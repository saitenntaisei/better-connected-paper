// Package handler is the Vercel Go Runtime entry point.
// vercel.json rewrites /api/* to /api/server so every request hits Handler;
// the chi router inside still sees the original URL path.
package handler

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/saitenntaisei/better-connected-paper/internal/api"
	"github.com/saitenntaisei/better-connected-paper/internal/citation"
	"github.com/saitenntaisei/better-connected-paper/internal/graph"
	"github.com/saitenntaisei/better-connected-paper/internal/store"
)

var (
	initOnce sync.Once
	handler  http.Handler
)

// Handler is the Vercel entry point. It lazily wires dependencies on the
// first request so cold starts pay the DB + S2 client setup cost once per
// instance and warm invocations are free.
func Handler(w http.ResponseWriter, r *http.Request) {
	initOnce.Do(initHandler)
	handler.ServeHTTP(w, r)
}

func initHandler() {
	ctx := context.Background()

	// DB is optional; failures are logged so ops see them in `vercel logs`
	// and /api/health surfaces the degraded state.
	var db *store.DB
	if dsn := os.Getenv("DATABASE_URL"); dsn == "" {
		log.Printf("init: DATABASE_URL unset — running without persistence cache")
	} else {
		openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		opened, err := store.Open(openCtx, dsn)
		cancel()
		if err != nil {
			log.Printf("init: store.Open failed, persistence cache disabled: %v", err)
		} else {
			db = opened
			migrateCtx, mc := context.WithTimeout(ctx, 30*time.Second)
			if err := db.Migrate(migrateCtx); err != nil {
				log.Printf("init: db.Migrate failed: %v", err)
			}
			mc()
		}
	}

	paperClient, recommender, embedder := newPaperClient()
	builder := &graph.Builder{S2: paperClient, Cache: graphCache(db), Recommender: recommender, Embedder: embedder}
	handler = api.NewRouter(api.Deps{S2: paperClient, DB: db, Builder: builder})
}

// paperClient satisfies both graph.S2 and api.PaperClient so either provider
// drops into Builder and handlers without casting.
type paperClient interface {
	graph.S2
	api.PaperClient
}

func newPaperClient() (paperClient, citation.Recommender, citation.Embedder) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("CITATION_PROVIDER")))
	switch provider {
	case "semanticscholar", "s2":
		log.Printf("init: citation provider = semanticscholar")
		c := citation.New(citation.Options{APIKey: os.Getenv("SEMANTIC_SCHOLAR_API_KEY")})
		return c, c, c
	case "openalex":
		log.Printf("init: citation provider = openalex")
		return citation.NewOpenAlex(citation.OpenAlexOptions{Mailto: os.Getenv("OPENALEX_EMAIL")}), nil, nil
	default:
		// Hybrid: OpenAlex primary + OpenCitations secondary, plus S2 tertiary
		// when a key is present (auto-enabled in vercelHybridTertiary). The
		// tertiary doubles as the Builder's Recommender + Embedder so sparse
		// seeds like brand-new arxiv preprints get the same /recommendations
		// + embedding-similarity wiring Connected Papers uses.
		primary := citation.NewOpenAlex(citation.OpenAlexOptions{Mailto: os.Getenv("OPENALEX_EMAIL")})
		if strings.EqualFold(os.Getenv("CITATION_HYBRID"), "false") {
			log.Printf("init: citation provider = openalex (hybrid disabled)")
			return primary, nil, nil
		}
		secondary := vercelHybridSecondary(primary)
		tertiary := vercelHybridTertiary(primary, secondary)
		if secondary == nil && tertiary == nil {
			log.Printf("init: citation provider = openalex (hybrid disabled; no supplement configured)")
			return primary, nil, nil
		}
		hc := &citation.HybridClient{Primary: primary, Secondary: secondary, Tertiary: tertiary}
		var rec citation.Recommender
		var emb citation.Embedder
		if tertiary != nil {
			rec = tertiary
			emb = tertiary
		}
		return hc, rec, emb
	}
}

// vercelHybridSecondary mirrors cmd/server.newHybridSecondary. Default is
// OpenCitations because its v2 index fills arxiv-DOI refs that OpenAlex
// drops, without S2's hostile anonymous rate limit.
func vercelHybridSecondary(primary *citation.OpenAlexClient) citation.PaperProvider {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CITATION_SECONDARY"))) {
	case "semanticscholar", "s2":
		log.Printf("init: citation provider = hybrid (openalex + semanticscholar secondary)")
		return citation.New(citation.Options{APIKey: os.Getenv("SEMANTIC_SCHOLAR_API_KEY")})
	case "none", "off":
		return nil
	default:
		log.Printf("init: citation provider = hybrid (openalex + opencitations secondary)")
		return citation.NewOpenCitations(citation.OpenCitationsOptions{
			Resolver: primary.ResolveByDOI,
			Mailto:   os.Getenv("OPENALEX_EMAIL"),
			Token:    os.Getenv("OPENCITATIONS_TOKEN"),
		})
	}
}

// vercelHybridTertiary mirrors cmd/server.newHybridTertiary. Auto-promotes
// to S2 whenever SEMANTIC_SCHOLAR_API_KEY is set so the recs endpoint is
// available for sparse seeds; without a key the default stays nil.
func vercelHybridTertiary(primary *citation.OpenAlexClient, secondary citation.PaperProvider) *citation.ResolvingTertiary {
	sel := strings.ToLower(strings.TrimSpace(os.Getenv("CITATION_TERTIARY")))
	if sel == "" && os.Getenv("SEMANTIC_SCHOLAR_API_KEY") != "" {
		sel = "semanticscholar"
		log.Printf("init: citation tertiary auto-enabled = semanticscholar (api key present)")
	}
	switch sel {
	case "semanticscholar", "s2":
		if _, isS2 := secondary.(*citation.Client); isS2 {
			return nil
		}
		log.Printf("init: citation provider = hybrid (+ semanticscholar tertiary)")
		return &citation.ResolvingTertiary{
			Inner:                citation.New(citation.Options{APIKey: os.Getenv("SEMANTIC_SCHOLAR_API_KEY")}),
			Resolver:             primary.ResolveByDOI,
			CiterSupplementLimit: 500,
		}
	default:
		return nil
	}
}

// graphCache returns the *store.DB as a graph.Cache only when it's non-nil,
// so the Builder sees a typed-nil-free interface value and its nil checks
// work as intended.
func graphCache(db *store.DB) graph.Cache {
	if db == nil {
		return nil
	}
	return db
}
