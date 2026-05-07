package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode"

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
		deduped := collapseOpenAlexDOIAliases(resp.Data)
		out := searchResponse{
			Total:   resp.Total,
			Results: make([]searchResult, 0, len(deduped)),
		}
		for _, p := range deduped {
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

// collapseOpenAlexDOIAliases drops the no-DOI half of OpenAlex's duplicate
// Work records for the same arxiv preprint. OpenAlex frequently emits two
// W-IDs per preprint — one carrying the arxiv DOI, one without — and the
// DOI-less alias has no way to bridge into the hybrid supplement chain
// (the seed-only AsyncVLA case). When a (title, year) group contains both
// DOI-bearing and DOI-less entries, only the DOI-less ones are removed;
// every DOI-bearing entry is kept, since two genuinely-distinct papers can
// share a title+year and the DOI is the only signal we have that the
// no-DOI member is an OpenAlex import alias rather than a real result.
// Groups that are uniformly DOI or uniformly no-DOI are left untouched.
func collapseOpenAlexDOIAliases(papers []citation.Paper) []citation.Paper {
	if len(papers) < 2 {
		return papers
	}

	type group struct {
		anyDOI   bool
		anyNoDOI bool
	}
	groups := make(map[string]*group, len(papers))
	keys := make([]string, len(papers))

	for i, p := range papers {
		key := dedupeKey(p.Title, p.Year)
		keys[i] = key
		if key == "" {
			continue
		}
		g, ok := groups[key]
		if !ok {
			g = &group{}
			groups[key] = g
		}
		if paperHasDOI(p) {
			g.anyDOI = true
		} else {
			g.anyNoDOI = true
		}
	}

	out := make([]citation.Paper, 0, len(papers))
	for i, p := range papers {
		key := keys[i]
		if key == "" {
			out = append(out, p)
			continue
		}
		g := groups[key]
		if !(g.anyDOI && g.anyNoDOI) {
			out = append(out, p)
			continue
		}
		if paperHasDOI(p) {
			out = append(out, p)
		}
	}
	return out
}

func dedupeKey(title string, year int) string {
	nt := normalizeTitleForDedupe(title)
	if nt == "" || year == 0 {
		return ""
	}
	return nt + "|" + strconv.Itoa(year)
}

func normalizeTitleForDedupe(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func paperHasDOI(p citation.Paper) bool {
	return strings.TrimSpace(p.ExternalIDs["DOI"]) != ""
}
