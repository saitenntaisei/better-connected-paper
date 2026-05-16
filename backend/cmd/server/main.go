package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/saitenntaisei/better-connected-paper/internal/api"
	"github.com/saitenntaisei/better-connected-paper/internal/citation"
	"github.com/saitenntaisei/better-connected-paper/internal/graph"
	"github.com/saitenntaisei/better-connected-paper/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	migrateOnly := flag.Bool("migrate-only", false, "apply migrations and exit")
	flag.Parse()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var db *store.DB
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		openCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		var err error
		db, err = store.Open(openCtx, dsn)
		cancel()
		if err != nil {
			logger.Error("db open failed", "err", err)
			os.Exit(1)
		}
		defer db.Close()

		migrateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := db.Migrate(migrateCtx); err != nil {
			cancel()
			logger.Error("migrate failed", "err", err)
			os.Exit(1)
		}
		cancel()
		logger.Info("db migrations applied")
	} else if *migrateOnly {
		logger.Error("-migrate-only requires DATABASE_URL")
		os.Exit(1)
	} else {
		logger.Warn("DATABASE_URL not set; running without persistence cache")
	}

	if *migrateOnly {
		return
	}

	paperClient, recommender, embedder := newPaperClient(logger)
	var cache graph.Cache
	if db != nil {
		cache = db
	}
	builder := &graph.Builder{
		S2:          paperClient,
		Cache:       cache,
		Recommender: recommender,
		Embedder:    embedder,
		Logger:      logger,
		// Long-running server can outlive the response; spawn a background
		// goroutine after the sync build to populate ar5iv-sourced refs so
		// the next request for the same seed serves the enriched graph.
		// Vercel api/server.go leaves this off — serverless functions
		// can't keep a goroutine alive past the response. Cache must be
		// available too: without a place to StoreGraph the enriched
		// payload, the background work would be discarded and the next
		// request would rebuild from scratch.
		DeferAr5iv: cache != nil && !strings.EqualFold(os.Getenv("DEFER_AR5IV"), "false"),
	}
	deps := api.Deps{S2: paperClient, DB: db, Builder: builder}

	srv := &http.Server{
		Addr:              net.JoinHostPort("0.0.0.0", port),
		Handler:           api.NewRouter(deps),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      300 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}

// paperClient satisfies both graph.S2 and api.PaperClient, so either provider
// implementation can be dropped into Builder and handlers without casting.
type paperClient interface {
	graph.S2
	api.PaperClient
}

func newPaperClient(logger *slog.Logger) (paperClient, citation.Recommender, citation.Embedder) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("CITATION_PROVIDER")))
	switch provider {
	case "semanticscholar", "s2":
		logger.Info("citation provider", "provider", "semanticscholar")
		c := citation.New(citation.Options{APIKey: os.Getenv("SEMANTIC_SCHOLAR_API_KEY")})
		return c, c, c
	case "openalex":
		logger.Info("citation provider", "provider", "openalex")
		return citation.NewOpenAlex(citation.OpenAlexOptions{Mailto: os.Getenv("OPENALEX_EMAIL")}), nil, nil
	default:
		// Default: OpenAlex primary with an OpenCitations supplement. OpenAlex
		// skips ref parsing for many arxiv preprints (referenced_works_count
		// comes back 0), and OpenCitations' v2 index fills that gap. Tertiary
		// auto-enables to S2 whenever a key is available (newHybridTertiary)
		// — the keyed 1 RPS limit is workable on the supplement path, and
		// exposes the /recommendations + embeddings endpoints that the
		// Builder leans on for sparse-seed expansion and edge wiring.
		primary := citation.NewOpenAlex(citation.OpenAlexOptions{Mailto: os.Getenv("OPENALEX_EMAIL")})
		disableHybrid := strings.EqualFold(os.Getenv("CITATION_HYBRID"), "false")
		if disableHybrid {
			logger.Info("citation provider", "provider", "openalex", "hybrid", false)
			return primary, nil, nil
		}
		secondary := newHybridSecondary(logger, primary)
		tertiary := newHybridTertiary(logger, primary, secondary)
		if secondary == nil && tertiary == nil {
			logger.Info("citation provider", "provider", "openalex", "hybrid", false)
			return primary, nil, nil
		}
		hc := &citation.HybridClient{
			Primary:   primary,
			Secondary: secondary,
			Tertiary:  tertiary,
			Logger:    logger,
		}
		var rec citation.Recommender
		var emb citation.Embedder
		if tertiary != nil {
			rec = tertiary
			emb = tertiary
		}
		return hc, rec, emb
	}
}

// newHybridSecondary builds the secondary provider wired into HybridClient.
// Defaults to OpenCitations; flipping CITATION_SECONDARY=semanticscholar
// restores the old S2 supplement (kept as an emergency fallback while the
// OpenCitations rollout beds in).
func newHybridSecondary(logger *slog.Logger, primary *citation.OpenAlexClient) citation.PaperProvider {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CITATION_SECONDARY"))) {
	case "semanticscholar", "s2":
		logger.Info("citation provider", "provider", "hybrid", "primary", "openalex", "secondary", "semanticscholar")
		return citation.New(citation.Options{APIKey: os.Getenv("SEMANTIC_SCHOLAR_API_KEY")})
	case "none", "off":
		return nil
	default:
		logger.Info("citation provider", "provider", "hybrid", "primary", "openalex", "secondary", "opencitations")
		return citation.NewOpenCitations(citation.OpenCitationsOptions{
			Resolver: primary.ResolveByDOI,
			Mailto:   os.Getenv("OPENALEX_EMAIL"),
			Token:    os.Getenv("OPENCITATIONS_TOKEN"),
		})
	}
}

// newHybridTertiary builds the optional tertiary provider wired into
// HybridClient. Auto-promotes to S2 when SEMANTIC_SCHOLAR_API_KEY is set
// (the 1 RPS keyed limit is workable on the supplement path and unlocks
// the /recommendations endpoint for sparse-seed graphs). Set
// CITATION_TERTIARY=off to opt out; without a key, default stays nil so
// anonymous 1-req/3-s S2 doesn't stall every cold build.
func newHybridTertiary(logger *slog.Logger, primary *citation.OpenAlexClient, secondary citation.PaperProvider) *citation.ResolvingTertiary {
	sel := strings.ToLower(strings.TrimSpace(os.Getenv("CITATION_TERTIARY")))
	if sel == "" && os.Getenv("SEMANTIC_SCHOLAR_API_KEY") != "" {
		sel = "semanticscholar"
		logger.Info("citation provider", "tertiary-default", "semanticscholar", "reason", "api-key-present")
	}
	switch sel {
	case "semanticscholar", "s2":
		// Skip if secondary is already S2 — avoids paying the S2 rate limit twice.
		if _, isS2 := secondary.(*citation.Client); isS2 {
			return nil
		}
		logger.Info("citation provider", "provider", "hybrid", "tertiary", "semanticscholar")
		var ar5iv citation.ArxivRefsFetcher
		if !strings.EqualFold(os.Getenv("CITATION_AR5IV"), "off") {
			ar5iv = citation.NewAr5ivClient(citation.Ar5ivOptions{})
			logger.Info("citation provider", "ar5iv-fallback", "enabled")
		}
		return &citation.ResolvingTertiary{
			Inner:    citation.New(citation.Options{APIKey: os.Getenv("SEMANTIC_SCHOLAR_API_KEY")}),
			Resolver: primary.ResolveByDOI,
			Logger:   logger,
			// Octo (1140 citers) is the canonical case: inline cuts off at 1000
			// and the 2024 robotics cluster (π0, OpenVLA, DROID, CrossFormer,
			// RDT) lives in offset 900-1100 of the paginated order. 500 covers
			// that band on the anon S2 tier (~15 s, one-shot per seed).
			CiterSupplementLimit: 500,
			Ar5iv:                ar5iv,
		}
	default:
		return nil
	}
}
