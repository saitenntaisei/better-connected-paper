package citation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(Options{
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
		MaxRetries: 2,
	})
	// loosen limiter for tests
	c.limiter = rate.NewLimiter(rate.Inf, 1)
	return c
}

func TestSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/paper/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != "transformers" {
			t.Errorf("unexpected query %q", r.URL.Query().Get("query"))
		}
		_ = json.NewEncoder(w).Encode(SearchResponse{
			Total: 1,
			Data:  []Paper{{PaperID: "abc", Title: "Attention Is All You Need", Year: 2017}},
		})
	})
	c := newTestClient(t, mux)

	resp, err := c.Search(context.Background(), "transformers", 5, DefaultPaperFields)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].PaperID != "abc" {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestEmbeddingsByExternalID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/paper/batch", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("fields"); got != "paperId,externalIds,embedding.specter_v2" {
			t.Errorf("fields query: got %q", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"paperId": "p1", "externalIds": map[string]string{"DOI": "10.1/A"}, "embedding": map[string]any{"vector": []float32{0.1, 0.2, 0.3}}},
			{"paperId": "p2", "externalIds": map[string]string{"DOI": "10.1/b"}, "embedding": map[string]any{"vector": []float32{0.4, 0.5, 0.6}}},
			nil, // 3rd id was missing in S2 — must be skipped, not error
			{"paperId": "p3", "externalIds": map[string]string{"DOI": "10.1/c"}}, // no embedding — skipped
		})
	})
	c := newTestClient(t, mux)

	got, err := c.EmbeddingsByExternalID(context.Background(), []string{"DOI:10.1/A", "DOI:10.1/b", "DOI:10.1/missing", "DOI:10.1/c"})
	if err != nil {
		t.Fatalf("embeddings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 embeddings (only p1+p2 have vectors), got %d: %v", len(got), got)
	}
	// Keys mirror the caller's input ids (positional match against the
	// batch response) so callers can register multiple lookups per paper —
	// "DOI:..." and "ARXIV:..." — and consult either form.
	if v, ok := got["DOI:10.1/A"]; !ok || len(v) != 3 {
		t.Errorf("p1 missing or wrong dim: %+v", got)
	}
	if _, ok := got["DOI:10.1/b"]; !ok {
		t.Errorf("p2 missing")
	}
	if _, ok := got["DOI:10.1/missing"]; ok {
		t.Errorf("null entry must not produce an embedding")
	}
	if _, ok := got["DOI:10.1/c"]; ok {
		t.Errorf("entry without embedding must not appear")
	}
}

// GetReferencesSinglePage must hit S2 exactly once even on a 429: the
// per-build refs-budget assumes one logical call equals one rate-limited
// slot, and the previous do() retry loop would silently burn 1 +
// maxRetries slots (each waiting up to 30 s on Retry-After) on this
// supplement step.
func TestGetReferencesSinglePageDoesNotRetryOn429(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/paper/X/references", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c := newTestClient(t, mux)

	start := time.Now()
	_, err := c.GetReferencesSinglePage(context.Background(), "X", []string{"paperId"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want error on 429, got nil")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("HTTP attempts: got %d, want 1 (no-retry contract)", got)
	}
	// 30 s Retry-After must NOT be honoured when the budget allowed only
	// one attempt — keeping wall-clock under a few seconds is the whole
	// point of the no-retry path.
	if elapsed > 5*time.Second {
		t.Errorf("getOnce should not sleep on terminal 429, elapsed %v", elapsed)
	}
}

func TestRecommend(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/recommendations/v1/papers/forpaper/SEED", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "20" {
			t.Errorf("limit got %q want 20", r.URL.Query().Get("limit"))
		}
		if r.URL.Query().Get("fields") != "paperId,externalIds,title" {
			t.Errorf("fields got %q", r.URL.Query().Get("fields"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"recommendedPapers": []Paper{
				{PaperID: "rec1", Title: "ProbeFlow", ExternalIDs: ExternalIDs{"ArXiv": "2511.99001"}},
				{PaperID: "rec2", Title: "AR-VLA", ExternalIDs: ExternalIDs{"ArXiv": "2511.99002", "DOI": "10.48550/arxiv.2511.99002"}},
			},
		})
	})
	c := newTestClient(t, mux)

	got, err := c.Recommend(context.Background(), "SEED", 20, []string{"paperId", "externalIds", "title"})
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if len(got) != 2 || got[0].PaperID != "rec1" || got[1].PaperID != "rec2" {
		t.Fatalf("unexpected: %+v", got)
	}
	if got[0].ExternalIDs["ArXiv"] != "2511.99001" {
		t.Errorf("want ArXiv preserved, got %v", got[0].ExternalIDs)
	}
}

func TestGetPaperNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/paper/missing", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	c := newTestClient(t, mux)

	if _, err := c.GetPaper(context.Background(), "missing", DefaultPaperFields); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGetReferencesPagination(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/paper/P1/references", func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		offset := r.URL.Query().Get("offset")
		switch {
		case n == 1 && offset == "0":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"offset": 0,
				"next":   2,
				"data": []map[string]any{
					{"citedPaper": map[string]any{"paperId": "a", "title": "A"}},
					{"citedPaper": map[string]any{"paperId": "b", "title": "B"}},
				},
			})
		case n == 2 && offset == "2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"offset": 2,
				"data": []map[string]any{
					{"citedPaper": map[string]any{"paperId": "c", "title": "C"}},
				},
			})
		default:
			t.Errorf("unexpected call %d offset=%s", n, offset)
		}
	})
	c := newTestClient(t, mux)

	papers, err := c.GetReferences(context.Background(), "P1", 5, DefaultPaperFields)
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	if len(papers) != 3 {
		t.Fatalf("want 3 papers, got %d: %+v", len(papers), papers)
	}
	if papers[0].PaperID != "a" || papers[2].PaperID != "c" {
		t.Errorf("unexpected order: %+v", papers)
	}
}

func TestGetCitations(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/paper/P1/citations", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"citingPaper": map[string]any{"paperId": "x", "title": "X"}},
			},
		})
	})
	c := newTestClient(t, mux)

	papers, err := c.GetCitations(context.Background(), "P1", 5, DefaultPaperFields)
	if err != nil {
		t.Fatalf("cites: %v", err)
	}
	if len(papers) != 1 || papers[0].PaperID != "x" {
		t.Fatalf("unexpected: %+v", papers)
	}
}

func TestRetryOn429(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/paper/P1", func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(Paper{PaperID: "P1", Title: "ok"})
	})
	c := newTestClient(t, mux)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, err := c.GetPaper(ctx, "P1", DefaultPaperFields)
	if err != nil {
		t.Fatalf("want success after retry, got %v", err)
	}
	if p.PaperID != "P1" {
		t.Fatalf("unexpected: %+v", p)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"":         0,
		"  ":       0,
		"0":        0,
		"3":        3 * time.Second,
		"30":       30 * time.Second,
		"nonsense": 0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
	// HTTP-date path.
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got < 3*time.Second || got > 6*time.Second {
		t.Errorf("parseRetryAfter(http-date) = %v, want ~5s", got)
	}
}

func TestExternalIDsMixedTypes(t *testing.T) {
	raw := []byte(`{"externalIds":{"DOI":"10.1/x","CorpusId":12345,"ArXiv":"2301.00001"}}`)
	var p Paper
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.ExternalIDs["DOI"] != "10.1/x" {
		t.Errorf("DOI: %q", p.ExternalIDs["DOI"])
	}
	if p.ExternalIDs["CorpusId"] != "12345" {
		t.Errorf("CorpusId: %q", p.ExternalIDs["CorpusId"])
	}
	if p.ExternalIDs["ArXiv"] != "2301.00001" {
		t.Errorf("ArXiv: %q", p.ExternalIDs["ArXiv"])
	}
}

func TestBatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/paper/batch", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"A"`) {
			t.Errorf("missing ids in %s", body)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"paperId": "A", "title": "alpha"},
			nil,
			{"paperId": "B", "title": "beta"},
		})
	})
	c := newTestClient(t, mux)

	papers, err := c.GetPaperBatch(context.Background(), []string{"A", "missing", "B"}, DefaultPaperFields)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(papers) != 2 {
		t.Fatalf("want 2 non-nil, got %d: %+v", len(papers), papers)
	}
}
