// Package handler is the Vercel Go Runtime entry point.
// vercel.json rewrites /api/* to /api/server so every request hits Handler;
// the chi router inside still sees the original URL path.
package handler

import (
	"context"
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

	var db *store.DB
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if opened, err := store.Open(openCtx, dsn); err == nil {
			db = opened
			migrateCtx, mc := context.WithTimeout(ctx, 30*time.Second)
			_ = db.Migrate(migrateCtx)
			mc()
		}
	}

	s2 := citation.New(citation.Options{APIKey: os.Getenv("SEMANTIC_SCHOLAR_API_KEY")})
	builder := &graph.Builder{S2: s2}
	handler = api.NewRouter(api.Deps{S2: s2, DB: db, Builder: builder})
}
