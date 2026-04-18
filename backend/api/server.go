// Package handler is the Vercel Go Runtime entry point.
// vercel.json rewrites /api/* to /api/server so every request hits Handler;
// the chi router inside still sees the original URL path.
package handler

import (
	"context"
	"log"
	"net/http"
	"os"
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

	s2 := citation.New(citation.Options{APIKey: os.Getenv("SEMANTIC_SCHOLAR_API_KEY")})
	builder := &graph.Builder{S2: s2, Cache: graphCache(db)}
	handler = api.NewRouter(api.Deps{S2: s2, DB: db, Builder: builder})
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
