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

	paperClient := newPaperClient(logger)
	var cache graph.Cache
	if db != nil {
		cache = db
	}
	builder := &graph.Builder{S2: paperClient, Cache: cache}
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

func newPaperClient(logger *slog.Logger) paperClient {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("CITATION_PROVIDER")))
	switch provider {
	case "semanticscholar", "s2":
		logger.Info("citation provider", "provider", "semanticscholar")
		return citation.New(citation.Options{APIKey: os.Getenv("SEMANTIC_SCHOLAR_API_KEY")})
	case "openalex":
		logger.Info("citation provider", "provider", "openalex")
		return citation.NewOpenAlex(citation.OpenAlexOptions{Mailto: os.Getenv("OPENALEX_EMAIL")})
	default:
		// Default: OpenAlex primary with an OpenCitations supplement. OpenAlex
		// skips ref parsing for many arxiv preprints (referenced_works_count
		// comes back 0), and OpenCitations' v2 index fills that gap without
		// S2's hostile 1-req/3-s anonymous rate limit. Tertiary is off by
		// default — S2 is opt-in via CITATION_TERTIARY=semanticscholar only,
		// so the hot path never blocks on S2's anonymous rate limit.
		primary := citation.NewOpenAlex(citation.OpenAlexOptions{Mailto: os.Getenv("OPENALEX_EMAIL")})
		disableHybrid := strings.EqualFold(os.Getenv("CITATION_HYBRID"), "false")
		if disableHybrid {
			logger.Info("citation provider", "provider", "openalex", "hybrid", false)
			return primary
		}
		secondary := newHybridSecondary(logger, primary)
		tertiary := newHybridTertiary(logger, primary, secondary)
		if secondary == nil && tertiary == nil {
			logger.Info("citation provider", "provider", "openalex", "hybrid", false)
			return primary
		}
		return &citation.HybridClient{
			Primary:   primary,
			Secondary: secondary,
			Tertiary:  tertiary,
			Logger:    logger,
		}
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
// HybridClient. Default is nil: primary (OpenAlex) + secondary
// (OpenCitations) already cover the common case, and S2 — the only
// provider that uniquely filled the arxiv-preprint gap — has an anonymous
// rate limit (1 req / 3 s) that turned supplement calls into stalls and
// 429 cascades during every cold graph build. Set
// CITATION_TERTIARY=semanticscholar to opt back in when you have an API
// key that raises that ceiling.
func newHybridTertiary(logger *slog.Logger, primary *citation.OpenAlexClient, secondary citation.PaperProvider) citation.PaperProvider {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CITATION_TERTIARY"))) {
	case "semanticscholar", "s2":
		// Skip if secondary is already S2 — avoids paying the S2 rate limit twice.
		if _, isS2 := secondary.(*citation.Client); isS2 {
			return nil
		}
		logger.Info("citation provider", "provider", "hybrid", "tertiary", "semanticscholar")
		return &citation.ResolvingTertiary{
			Inner:    citation.New(citation.Options{APIKey: os.Getenv("SEMANTIC_SCHOLAR_API_KEY")}),
			Resolver: primary.ResolveByDOI,
			Logger:   logger,
			// Octo (1140 citers) is the canonical case: inline cuts off at 1000
			// and the 2024 robotics cluster (π0, OpenVLA, DROID, CrossFormer,
			// RDT) lives in offset 900-1100 of the paginated order. 500 covers
			// that band on the anon S2 tier (~15 s, one-shot per seed).
			CiterSupplementLimit: 500,
		}
	default:
		return nil
	}
}
