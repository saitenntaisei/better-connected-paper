package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/saitenntaisei/better-connected-paper/internal/citation"
	"github.com/saitenntaisei/better-connected-paper/internal/graph"
	"github.com/saitenntaisei/better-connected-paper/internal/store"
)

// buildRequest is the POST body of /api/graph/build.
type buildRequest struct {
	SeedID string `json:"seedId"`
	Fresh  bool   `json:"fresh,omitempty"` // set true to bypass cache
}

// BuildGraph handles POST /api/graph/build.
// The response is the same JSON shape the frontend expects, whether we hit the cache or build fresh.
func (d Deps) BuildGraph(w http.ResponseWriter, r *http.Request) {
	if d.Builder == nil {
		WriteError(w, http.StatusServiceUnavailable, "graph builder unavailable")
		return
	}

	var req buildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	seed := strings.TrimSpace(req.SeedID)
	if seed == "" {
		WriteError(w, http.StatusBadRequest, "seedId required")
		return
	}

	if !req.Fresh {
		if cached, err := d.DB.GetGraph(r.Context(), seed); err == nil {
			w.Header().Set("X-Cache", "hit")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(cached)
			return
		}
	}

	build := func() (*graph.Response, error) { return d.Builder.Build(r.Context(), seed) }
	var (
		resp *graph.Response
		err  error
	)
	if d.SFlight != nil {
		v, sErr, _ := d.SFlight.Do("graph:"+seed, func() (any, error) { return build() })
		if sErr == nil {
			resp = v.(*graph.Response)
		}
		err = sErr
	} else {
		resp, err = build()
	}
	if errors.Is(err, citation.ErrNotFound) {
		WriteError(w, http.StatusNotFound, "seed paper not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusBadGateway, "build failed: "+err.Error())
		return
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "marshal: "+err.Error())
		return
	}
	if err := d.DB.PutGraph(r.Context(), seed, payload, store.DefaultGraphTTL); err != nil {
		w.Header().Set("X-Cache-Write-Error", err.Error())
	}

	w.Header().Set("X-Cache", "miss")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

// GetGraph handles GET /api/graph/{seedId} — cache lookup only, 404 if not built yet.
func (d Deps) GetGraph(w http.ResponseWriter, r *http.Request) {
	seed := strings.TrimSpace(chi.URLParam(r, "seedId"))
	if seed == "" {
		WriteError(w, http.StatusBadRequest, "seedId required")
		return
	}
	cached, err := d.DB.GetGraph(r.Context(), seed)
	if errors.Is(err, store.ErrNotFound) {
		WriteError(w, http.StatusNotFound, "graph not cached; POST /api/graph/build first")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("X-Cache", "hit")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cached)
}
