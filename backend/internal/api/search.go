package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/saitenntaisei/better-connected-paper/internal/citation"
)

// searchResult is a compact shape the frontend's results list consumes.
type searchResult struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Year          int      `json:"year,omitempty"`
	Authors       []string `json:"authors,omitempty"`
	Venue         string   `json:"venue,omitempty"`
	CitationCount int      `json:"citationCount,omitempty"`
	Abstract      string   `json:"abstract,omitempty"`
}

type searchResponse struct {
	Total   int            `json:"total"`
	Results []searchResult `json:"results"`
}

// searchFields are the S2 fields we need for the results list.
var searchFields = []string{
	"paperId", "title", "year", "authors", "venue", "citationCount", "abstract",
}

// Search proxies /api/search?q=&limit=10 to Semantic Scholar.
func (d Deps) Search(w http.ResponseWriter, r *http.Request) {
	if d.S2 == nil {
		WriteError(w, http.StatusServiceUnavailable, "search backend unavailable")
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		WriteError(w, http.StatusBadRequest, "q parameter required")
		return
	}
	limit := 10
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			WriteError(w, http.StatusBadRequest, "limit must be an integer in [1,50]")
			return
		}
		limit = parsed
	}

	key := searchCacheKey(q, limit)
	if cached, ok := d.SearchCache.get(key); ok {
		w.Header().Set("X-Cache", "hit")
		WriteJSON(w, http.StatusOK, cached)
		return
	}

	out, err := d.doSearch(r.Context(), key, q, limit)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "semantic scholar: "+err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// doSearch coalesces concurrent identical searches and memoizes the result.
func (d Deps) doSearch(ctx context.Context, key, q string, limit int) (searchResponse, error) {
	fetch := func() (searchResponse, error) {
		resp, err := d.S2.Search(ctx, q, limit, searchFields)
		if err != nil {
			return searchResponse{}, err
		}
		out := searchResponse{
			Total:   resp.Total,
			Results: make([]searchResult, 0, len(resp.Data)),
		}
		for _, p := range resp.Data {
			out.Results = append(out.Results, toSearchResult(p))
		}
		d.SearchCache.put(key, out)
		return out, nil
	}
	if d.SFlight == nil {
		return fetch()
	}
	v, err, _ := d.SFlight.Do("search:"+key, func() (any, error) { return fetch() })
	if err != nil {
		return searchResponse{}, err
	}
	return v.(searchResponse), nil
}

func toSearchResult(p citation.Paper) searchResult {
	authors := make([]string, 0, len(p.Authors))
	for _, a := range p.Authors {
		if a.Name != "" {
			authors = append(authors, a.Name)
		}
	}
	return searchResult{
		ID:            p.PaperID,
		Title:         p.Title,
		Year:          p.Year,
		Authors:       authors,
		Venue:         p.Venue,
		CitationCount: p.CitationCount,
		Abstract:      truncateAbstract(p.Abstract, 280),
	}
}

func truncateAbstract(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
