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
