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
		w.Header().Set("X-Cache", "hit")
		WriteJSON(w, http.StatusOK, cached[0])
		return
	}

	fetch := func() (*citation.Paper, error) {
		// Re-check cache inside the singleflight lambda so a follower that
		// woke up after the leader's upsert still avoids a second S2 hit.
		if cached, _ := d.DB.GetPapers(r.Context(), []string{id}); len(cached) == 1 {
			p := cached[0]
			return &p, nil
		}
		p, err := d.S2.GetPaper(r.Context(), id, paperFields)
		if err != nil {
			return nil, err
		}
		if err := d.DB.UpsertPapers(r.Context(), []citation.Paper{*p}); err != nil {
			w.Header().Set("X-Cache-Write-Error", err.Error())
		}
		return p, nil
	}

	var (
		p   *citation.Paper
		err error
	)
	if d.SFlight != nil {
		v, sErr, _ := d.SFlight.Do("paper:"+id, func() (any, error) { return fetch() })
		if sErr == nil {
			p = v.(*citation.Paper)
		}
		err = sErr
	} else {
		p, err = fetch()
	}

	if errors.Is(err, citation.ErrNotFound) {
		WriteError(w, http.StatusNotFound, "paper not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusBadGateway, "semantic scholar: "+err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, p)
}
