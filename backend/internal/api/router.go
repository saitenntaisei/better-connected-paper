package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

type Router struct {
	chi.Router
}

// NewRouter wires middleware + all /api/* routes. Any field on deps may be nil;
// the relevant handler will respond with 503 when its dependency is missing.
func NewRouter(deps Deps) *Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(120 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://localhost:3000"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Accept"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/api/health", Health)
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
