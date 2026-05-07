package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saitenntaisei/better-connected-paper/internal/citation"
	"github.com/saitenntaisei/better-connected-paper/internal/graph"
)

// --- Stubs ---

type stubS2 struct {
	searchResp  *citation.SearchResponse
	searchErr   error
	searchCalls int
	paper       *citation.Paper
	paperErr    error
	paperCalls  int
}

func (s *stubS2) Search(_ context.Context, _ string, _ int, _ []string) (*citation.SearchResponse, error) {
	s.searchCalls++
	return s.searchResp, s.searchErr
}
func (s *stubS2) GetPaper(_ context.Context, _ string, _ []string) (*citation.Paper, error) {
	s.paperCalls++
	return s.paper, s.paperErr
}

type stubBuilder struct {
	resp *graph.Response
	err  error
	seen string
}

func (b *stubBuilder) Build(_ context.Context, seedID string) (*graph.Response, error) {
	b.seen = seedID
	return b.resp, b.err
}

// --- Tests ---

func TestSearchValidation(t *testing.T) {
	d := Deps{S2: &stubS2{searchResp: &citation.SearchResponse{}}}
	r := NewRouter(d)

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"missing q", "/api/search", http.StatusBadRequest},
		{"limit too big", "/api/search?q=foo&limit=500", http.StatusBadRequest},
		{"limit not int", "/api/search?q=foo&limit=abc", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rec.Code != tc.want {
				t.Errorf("got %d, want %d, body=%s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestSearchHappyPath(t *testing.T) {
	d := Deps{S2: &stubS2{searchResp: &citation.SearchResponse{
		Total: 1,
		Data: []citation.Paper{{
			PaperID: "abc", Title: "Attention Is All You Need", Year: 2017,
			Authors: []citation.Author{{Name: "Ashish"}}, CitationCount: 90000,
		}},
	}}}
	r := NewRouter(d)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=transformers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, body=%s", rec.Code, rec.Body)
	}
	var got searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Total != 1 || len(got.Results) != 1 || got.Results[0].ID != "abc" {
		t.Fatalf("unexpected: %+v", got)
	}
}

// OpenAlex sometimes returns two W-IDs for the same arxiv preprint — one
// with the arxiv DOI, one without. The DOI-less alias has 0 refs/cites and
// the hybrid supplement chain can't bridge from it, so picking it yields a
// seed-only graph (the AsyncVLA "1 node only" report). Surface only the
// DOI-bearing record when the same (title, year) appears with mixed
// DOI/no-DOI.
func TestSearchCollapsesOpenAlexDOIAliases(t *testing.T) {
	d := Deps{S2: &stubS2{searchResp: &citation.SearchResponse{
		Total: 2,
		Data: []citation.Paper{
			{PaperID: "W7106207536", Title: "AsyncVLA: Asynchronous Flow Matching", Year: 2025, Venue: "ArXiv.org"},
			{PaperID: "W7106158755", Title: "AsyncVLA: Asynchronous Flow Matching", Year: 2025, Venue: "arXiv (Cornell University)", ExternalIDs: citation.ExternalIDs{"DOI": "10.48550/arxiv.2511.14148"}},
		},
	}}}
	r := NewRouter(d)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=AsyncVLA", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var got searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Results) != 1 {
		t.Fatalf("want 1 deduped result, got %d: %+v", len(got.Results), got.Results)
	}
	if got.Results[0].ID != "W7106158755" {
		t.Errorf("want DOI-bearing W7106158755 kept, got %s", got.Results[0].ID)
	}
}

// Two genuinely-distinct papers can share a title (e.g. there are two
// "Attention Is All You Need" papers). When all entries in a (title, year)
// group already have DOIs, we have no signal that they're aliases — keep
// them all so we don't silently merge legitimate matches.
func TestSearchKeepsSameTitleWhenAllHaveDOIs(t *testing.T) {
	d := Deps{S2: &stubS2{searchResp: &citation.SearchResponse{
		Total: 2,
		Data: []citation.Paper{
			{PaperID: "A", Title: "Attention Is All You Need", Year: 2017, ExternalIDs: citation.ExternalIDs{"DOI": "10.1/a"}},
			{PaperID: "B", Title: "Attention Is All You Need", Year: 2017, ExternalIDs: citation.ExternalIDs{"DOI": "10.2/b"}},
		},
	}}}
	r := NewRouter(d)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=attention", nil))
	var got searchResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Results) != 2 {
		t.Errorf("want 2 results when both have DOIs, got %d: %+v", len(got.Results), got.Results)
	}
}

// When a (title, year) group has multiple DOI-bearing entries plus a
// no-DOI alias, every DOI entry is potentially a legitimate distinct paper.
// Only the no-DOI alias should be dropped — collapsing to a single
// "winner" would hide a real match the user might have intended to pick.
func TestSearchKeepsAllDOIBearingWhenAliasMixed(t *testing.T) {
	d := Deps{S2: &stubS2{searchResp: &citation.SearchResponse{
		Total: 3,
		Data: []citation.Paper{
			{PaperID: "A", Title: "Foo", Year: 2024, ExternalIDs: citation.ExternalIDs{"DOI": "10.1/a"}, CitationCount: 100},
			{PaperID: "B", Title: "Foo", Year: 2024},
			{PaperID: "C", Title: "Foo", Year: 2024, ExternalIDs: citation.ExternalIDs{"DOI": "10.2/c"}, CitationCount: 5},
		},
	}}}
	r := NewRouter(d)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=foo", nil))
	var got searchResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Results) != 2 {
		t.Fatalf("want 2 (only DOI-less alias B dropped), got %d: %+v", len(got.Results), got.Results)
	}
	ids := map[string]bool{}
	for _, r := range got.Results {
		ids[r.ID] = true
	}
	if !ids["A"] || !ids["C"] {
		t.Errorf("want both DOI-bearing A and C kept, got %v", ids)
	}
	if ids["B"] {
		t.Errorf("want DOI-less alias B dropped, but it is present")
	}
}

// Same title, different year (preprint vs conference version) is not a
// duplication artifact — keep both so users can pick the venue they want.
func TestSearchKeepsSameTitleDifferentYears(t *testing.T) {
	d := Deps{S2: &stubS2{searchResp: &citation.SearchResponse{
		Total: 2,
		Data: []citation.Paper{
			{PaperID: "P", Title: "Foo", Year: 2023},
			{PaperID: "Q", Title: "Foo", Year: 2024, ExternalIDs: citation.ExternalIDs{"DOI": "10.1/q"}},
		},
	}}}
	r := NewRouter(d)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=foo", nil))
	var got searchResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Results) != 2 {
		t.Errorf("want 2 results across distinct years, got %d", len(got.Results))
	}
}

// Second identical search hits the in-memory cache instead of S2.
func TestSearchCacheHit(t *testing.T) {
	s2 := &stubS2{searchResp: &citation.SearchResponse{
		Total: 1,
		Data:  []citation.Paper{{PaperID: "abc", Title: "t"}},
	}}
	r := NewRouter(Deps{S2: s2})

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=abc&limit=5", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("iter %d: code=%d body=%s", i, rec.Code, rec.Body)
		}
	}
	if s2.searchCalls != 1 {
		t.Errorf("S2.Search called %d times, want 1 (TTL cache should absorb repeats)", s2.searchCalls)
	}
}

func TestPaperNotFound(t *testing.T) {
	d := Deps{S2: &stubS2{paperErr: citation.ErrNotFound}}
	r := NewRouter(d)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/paper/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rec.Code, rec.Body)
	}
}

func TestPaperOk(t *testing.T) {
	d := Deps{S2: &stubS2{paper: &citation.Paper{PaperID: "P1", Title: "Hello"}}}
	r := NewRouter(d)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/paper/P1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got citation.Paper
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PaperID != "P1" {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestBuildGraphHappyPath(t *testing.T) {
	b := &stubBuilder{resp: &graph.Response{
		Seed:  graph.Node{ID: "S", Title: "Seed", IsSeed: true},
		Nodes: []graph.Node{{ID: "S", IsSeed: true}, {ID: "A", Similarity: 0.8}},
		Edges: []graph.Edge{{Source: "S", Target: "A", Kind: graph.EdgeCite, Weight: 1}},
	}}
	d := Deps{Builder: b}
	r := NewRouter(d)

	body := bytes.NewBufferString(`{"seedId":"S"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/graph/build", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Cache") != "miss" {
		t.Errorf("X-Cache header = %q, want miss", rec.Header().Get("X-Cache"))
	}
	if b.seen != "S" {
		t.Errorf("builder saw %q, want S", b.seen)
	}
	var got graph.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Edges) != 1 || got.Edges[0].Kind != graph.EdgeCite {
		t.Errorf("unexpected edges: %+v", got.Edges)
	}
}

func TestBuildGraphBadJSON(t *testing.T) {
	d := Deps{Builder: &stubBuilder{}}
	r := NewRouter(d)
	req := httptest.NewRequest(http.MethodPost, "/api/graph/build", bytes.NewBufferString("{"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestBuildGraphMissingSeed(t *testing.T) {
	d := Deps{Builder: &stubBuilder{}}
	r := NewRouter(d)
	req := httptest.NewRequest(http.MethodPost, "/api/graph/build", bytes.NewBufferString(`{"seedId":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestBuildGraphPropagatesNotFound(t *testing.T) {
	d := Deps{Builder: &stubBuilder{err: citation.ErrNotFound}}
	r := NewRouter(d)
	req := httptest.NewRequest(http.MethodPost, "/api/graph/build", bytes.NewBufferString(`{"seedId":"nope"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestGetGraphMissing(t *testing.T) {
	d := Deps{}
	r := NewRouter(d)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/graph/xyz", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestUnavailableDependencies(t *testing.T) {
	r := NewRouter(Deps{})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=x", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/graph/build", bytes.NewBufferString(`{"seedId":"S"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}

// compile-time sanity to ensure errors import is used
var _ = errors.New
