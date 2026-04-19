package api

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"golang.org/x/sync/singleflight"
)

type Router struct {
	chi.Router
}

var defaultLocalOrigins = []string{"http://localhost:5173", "http://localhost:3000"}

// resolveAllowedOrigins prefers the ALLOWED_ORIGINS env var (comma-separated).
// "*" allows any origin — useful for previews; never use with credentials.
// Empty env falls back to localhost defaults so dev keeps working.
func resolveAllowedOrigins(getenv func(string) string) []string {
	raw := strings.TrimSpace(getenv("ALLOWED_ORIGINS"))
	if raw == "" {
		return defaultLocalOrigins
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return defaultLocalOrigins
	}
	return out
}

// NewRouter wires middleware + all /api/* routes. Any field on deps may be nil;
// the relevant handler will respond with 503 when its dependency is missing.
// SFlight and SearchCache are provisioned here (once per process) if the
// caller didn't supply them, so production wiring and tests stay simple.
func NewRouter(deps Deps) *Router {
	if deps.SFlight == nil {
		deps.SFlight = &singleflight.Group{}
	}
	if deps.SearchCache == nil {
		deps.SearchCache = newSearchCache()
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(240 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   resolveAllowedOrigins(os.Getenv),
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Accept"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/api/health", deps.Health)
	r.Get("/api/search", deps.Search)
	r.Get("/api/paper/{id}", deps.Paper)
	r.Post("/api/graph/build", deps.BuildGraph)
	r.Get("/api/graph/{seedId}", deps.GetGraph)

	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusNotFound, "not found")
	})

	return &Router{Router: r}
}
