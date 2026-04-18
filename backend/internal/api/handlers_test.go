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
