package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/saitenntaisei/better-connected-paper/internal/citation"
)

// paperFields are the full set returned for a single-paper details request.
var paperFields = []string{
	"paperId", "title", "abstract", "year", "venue", "authors",
	"citationCount", "referenceCount", "influentialCitationCount",
	"externalIds", "url",
}

// Paper handles GET /api/paper/{id} — returns one paper's metadata.
func (d Deps) Paper(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "id required")
		return
	}
	if d.S2 == nil {
		WriteError(w, http.StatusServiceUnavailable, "paper lookup unavailable")
		return
	}

	// Try cache first (best-effort).
	if cached, _ := d.DB.GetPapers(r.Context(), []string{id}); len(cached) == 1 {
		WriteJSON(w, http.StatusOK, cached[0])
		return
	}

	p, err := d.S2.GetPaper(r.Context(), id, paperFields)
	if errors.Is(err, citation.ErrNotFound) {
		WriteError(w, http.StatusNotFound, "paper not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusBadGateway, "semantic scholar: "+err.Error())
		return
	}

	if err := d.DB.UpsertPapers(r.Context(), []citation.Paper{*p}); err != nil {
		// Cache failures shouldn't break the response; log-style write to response headers.
		w.Header().Set("X-Cache-Write-Error", err.Error())
	}
	WriteJSON(w, http.StatusOK, p)
}
