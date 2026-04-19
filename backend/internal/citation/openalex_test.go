package citation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func newOpenAlexTestClient(t *testing.T, handler http.HandlerFunc) (*OpenAlexClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewOpenAlex(OpenAlexOptions{
		BaseURL: srv.URL,
		RPS:     1000, // tests shouldn't wait on the limiter
		Burst:   1000,
	}), srv
}

// Fixture: a minimal work payload covering every field we convert.
const fixtureWork = `{
  "id": "https://openalex.org/W42",
  "display_name": "Attention Is All You Need",
  "publication_year": 2017,
  "cited_by_count": 90000,
  "referenced_works_count": 2,
  "referenced_works": [
    "https://openalex.org/W10",
    "https://openalex.org/W20"
  ],
  "abstract_inverted_index": {
    "Attention": [0],
    "is": [1],
    "all": [2],
    "you": [3],
    "need": [4]
  },
  "primary_location": {
    "source": {"display_name": "NeurIPS"}
  },
  "authorships": [
    {"author": {"id": "https://openalex.org/A1", "display_name": "Ashish Vaswani"}},
    {"author": {"id": "https://openalex.org/A2", "display_name": "Noam Shazeer"}}
  ],
  "ids": {
    "openalex": "https://openalex.org/W42",
    "doi": "https://doi.org/10.48550/arXiv.1706.03762",
    "mag": "2962840149"
  }
}`

func TestOpenAlexGetPaper(t *testing.T) {
	var path atomic.Value
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.String())
		if !strings.HasPrefix(r.URL.Path, "/works/W42") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(fixtureWork))
	})

	p, err := c.GetPaper(context.Background(), "W42", nil)
	if err != nil {
		t.Fatalf("GetPaper: %v", err)
	}
	if p.PaperID != "W42" {
		t.Errorf("PaperID = %q, want W42", p.PaperID)
	}
	if p.Title != "Attention Is All You Need" {
		t.Errorf("Title = %q", p.Title)
	}
	if p.Year != 2017 {
		t.Errorf("Year = %d", p.Year)
	}
	if p.Venue != "NeurIPS" {
		t.Errorf("Venue = %q", p.Venue)
	}
	if p.CitationCount != 90000 || p.ReferenceCount != 2 {
		t.Errorf("counts wrong: %+v", p)
	}
	if p.Abstract != "Attention is all you need" {
		t.Errorf("abstract = %q", p.Abstract)
	}
	if len(p.Authors) != 2 || p.Authors[0].Name != "Ashish Vaswani" || p.Authors[0].AuthorID != "A1" {
		t.Errorf("authors = %+v", p.Authors)
	}
	if len(p.References) != 2 || p.References[0].PaperID != "W10" || p.References[1].PaperID != "W20" {
		t.Errorf("refs = %+v", p.References)
	}
	if got := p.ExternalIDs["DOI"]; got != "10.48550/arXiv.1706.03762" {
		t.Errorf("DOI = %q", got)
	}
	if p.URL != "https://doi.org/10.48550/arXiv.1706.03762" {
		t.Errorf("URL = %q, want DOI link", p.URL)
	}
	// select= parameter must be present — confirms we're asking for the full field set.
	if !strings.Contains(path.Load().(string), "select=") {
		t.Errorf("request missing select: %v", path.Load())
	}
}

func TestOpenAlexGetPaperFetchesCitesWhenAsked(t *testing.T) {
	// Use a low-cite fixture so the client's truncation guard lets cites
	// enrichment fire. Highly-cited papers are covered by a separate test.
	const lowCiteFixture = `{
	  "id": "https://openalex.org/W42",
	  "display_name": "Small Paper",
	  "cited_by_count": 2,
	  "referenced_works": []
	}`
	var citesCalls atomic.Int32
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/works/W42") {
			_, _ = w.Write([]byte(lowCiteFixture))
			return
		}
		if r.URL.Path == "/works" && strings.Contains(r.URL.RawQuery, "filter=cites%3AW42") {
			citesCalls.Add(1)
			_, _ = w.Write([]byte(`{"meta":{"count":2},"results":[
				{"id":"https://openalex.org/W100"},
				{"id":"https://openalex.org/W200"}
			]}`))
			return
		}
		http.NotFound(w, r)
	})

	p, err := c.GetPaper(context.Background(), "W42", []string{"paperId", "title", "citations.paperId"})
	if err != nil {
		t.Fatalf("GetPaper: %v", err)
	}
	if citesCalls.Load() != 1 {
		t.Errorf("cites fetch called %d times, want 1", citesCalls.Load())
	}
	if len(p.Citations) != 2 || p.Citations[0].PaperID != "W100" || p.Citations[1].PaperID != "W200" {
		t.Errorf("cites = %+v", p.Citations)
	}
}

func TestOpenAlexGetPaperSkipsCitesFetchWhenTruncationExpected(t *testing.T) {
	// fixtureWork has cited_by_count=90000, above openAlexCitesLimit.
	// GetPaper must not fire the cites query for a paper whose cites would
	// be truncated — leaving them cached partial corrupts co-citation scores.
	var citesCalls atomic.Int32
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/works/W42") {
			_, _ = w.Write([]byte(fixtureWork))
			return
		}
		if strings.Contains(r.URL.RawQuery, "filter=cites") {
			citesCalls.Add(1)
		}
		http.NotFound(w, r)
	})
	p, err := c.GetPaper(context.Background(), "W42", []string{"citations.paperId"})
	if err != nil {
		t.Fatalf("GetPaper: %v", err)
	}
	if citesCalls.Load() != 0 {
		t.Errorf("cites fired for high-cite paper: %d", citesCalls.Load())
	}
	if len(p.Citations) != 0 {
		t.Errorf("expected empty Citations, got %+v", p.Citations)
	}
	if !p.CitationsUnknown {
		t.Error("CitationsUnknown not set — cache layer will misread empty cites as complete")
	}
}

func TestOpenAlexGetPaperSkipsCitesWhenNotAsked(t *testing.T) {
	var citesCalls atomic.Int32
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/works/W42") {
			_, _ = w.Write([]byte(fixtureWork))
			return
		}
		if r.URL.Path == "/works" && strings.Contains(r.URL.RawQuery, "filter=cites") {
			citesCalls.Add(1)
		}
		_, _ = w.Write([]byte(`{"meta":{"count":0},"results":[]}`))
	})
	if _, err := c.GetPaper(context.Background(), "W42", []string{"paperId", "title"}); err != nil {
		t.Fatalf("GetPaper: %v", err)
	}
	if citesCalls.Load() != 0 {
		t.Errorf("cites fetch fired when not asked for: %d", citesCalls.Load())
	}
}

func TestOpenAlexSearch(t *testing.T) {
	var query atomic.Value
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.Query().Get("search"))
		_, _ = w.Write([]byte(`{"meta":{"count":1},"results":[` + fixtureWork + `]}`))
	})
	resp, err := c.Search(context.Background(), "transformers", 5, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Total != 1 || len(resp.Data) != 1 || resp.Data[0].PaperID != "W42" {
		t.Errorf("resp = %+v", resp)
	}
	if got := query.Load(); got != "transformers" {
		t.Errorf("search param = %q", got)
	}
}

func TestOpenAlexBatchEnrichesCitationsWhenAsked(t *testing.T) {
	var citesCalls atomic.Int32
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		switch {
		case strings.HasPrefix(filter, "openalex:"):
			_, _ = w.Write([]byte(`{"meta":{"count":2},"results":[
				{"id":"https://openalex.org/W1","display_name":"one","cited_by_count":5},
				{"id":"https://openalex.org/W2","display_name":"two","cited_by_count":3}
			]}`))
		case strings.HasPrefix(filter, "cites:"):
			citesCalls.Add(1)
			// Return a single citing paper keyed off which paper was asked.
			citing := strings.TrimPrefix(filter, "cites:") + "X"
			_, _ = w.Write([]byte(`{"meta":{"count":1},"results":[{"id":"https://openalex.org/` + citing + `"}]}`))
		default:
			http.NotFound(w, r)
		}
	})

	papers, err := c.GetPaperBatch(context.Background(), []string{"W1", "W2"}, []string{"paperId", "references.paperId", "citations.paperId"})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if citesCalls.Load() != 2 {
		t.Errorf("cites calls = %d, want 2 (one per paper)", citesCalls.Load())
	}
	if len(papers) != 2 {
		t.Fatalf("papers = %d", len(papers))
	}
	byID := map[string][]Paper{}
	for _, p := range papers {
		byID[p.PaperID] = p.Citations
	}
	if len(byID["W1"]) != 1 || byID["W1"][0].PaperID != "W1X" {
		t.Errorf("W1 citations = %+v, want [{W1X}]", byID["W1"])
	}
	if len(byID["W2"]) != 1 || byID["W2"][0].PaperID != "W2X" {
		t.Errorf("W2 citations = %+v", byID["W2"])
	}
}

func TestOpenAlexBatchSkipsCitesFetchWhenTruncationExpected(t *testing.T) {
	// Paper's cited_by_count (300) exceeds openAlexCitesLimit (100), so the
	// client must skip enrichment rather than persist a partial prefix.
	var citesCalls atomic.Int32
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		if strings.HasPrefix(filter, "cites:") {
			citesCalls.Add(1)
			_, _ = w.Write([]byte(`{"meta":{"count":300},"results":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"meta":{"count":1},"results":[
			{"id":"https://openalex.org/W1","display_name":"big","cited_by_count":300}
		]}`))
	})
	papers, err := c.GetPaperBatch(context.Background(), []string{"W1"}, []string{"citations.paperId"})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if citesCalls.Load() != 0 {
		t.Errorf("cites fetch fired on high-cite paper: %d (expected skip)", citesCalls.Load())
	}
	if len(papers) != 1 || len(papers[0].Citations) != 0 {
		t.Errorf("expected empty Citations on truncation-skipped paper, got %+v", papers[0].Citations)
	}
	if !papers[0].CitationsUnknown {
		t.Error("CitationsUnknown not set — cache layer will misread empty cites as complete")
	}
}

func TestOpenAlexFetchCitesDiscardsTruncatedResponse(t *testing.T) {
	// OpenAlex reports 1000 matches but returns only 100 — we must not hand
	// that truncated prefix to the caller, which would cache it as complete.
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{"count":1000},"results":[
			{"id":"https://openalex.org/W100"},
			{"id":"https://openalex.org/W101"}
		]}`))
	})
	cites, err := c.fetchCitesList(context.Background(), "W1", 100)
	if err != nil {
		t.Fatalf("fetchCitesList: %v", err)
	}
	if cites != nil {
		t.Errorf("expected nil on truncation, got %+v", cites)
	}
}

func TestOpenAlexBatchSkipsCitesFetchForZeroCitedPapers(t *testing.T) {
	var citesCalls atomic.Int32
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		if strings.HasPrefix(filter, "cites:") {
			citesCalls.Add(1)
			_, _ = w.Write([]byte(`{"meta":{"count":0},"results":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"meta":{"count":2},"results":[
			{"id":"https://openalex.org/W1","display_name":"zero","cited_by_count":0},
			{"id":"https://openalex.org/W2","display_name":"two","cited_by_count":7}
		]}`))
	})
	if _, err := c.GetPaperBatch(context.Background(), []string{"W1", "W2"}, []string{"citations.paperId"}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if citesCalls.Load() != 1 {
		t.Errorf("cites calls = %d, want 1 (W1 should be skipped)", citesCalls.Load())
	}
}

func TestOpenAlexBatchSkipsCitationsWhenNotAsked(t *testing.T) {
	var citesCalls atomic.Int32
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		if strings.HasPrefix(filter, "cites:") {
			citesCalls.Add(1)
		}
		_, _ = w.Write([]byte(`{"meta":{"count":1},"results":[{"id":"https://openalex.org/W1","display_name":"one"}]}`))
	})
	if _, err := c.GetPaperBatch(context.Background(), []string{"W1"}, []string{"paperId", "references.paperId"}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if citesCalls.Load() != 0 {
		t.Errorf("cites fired without citations.* field: %d", citesCalls.Load())
	}
}

func TestOpenAlexBatchChunking(t *testing.T) {
	var calls atomic.Int32
	c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		filter := r.URL.Query().Get("filter")
		ids := strings.Split(strings.TrimPrefix(filter, "openalex:"), "|")
		results := make([]json.RawMessage, 0, len(ids))
		for _, id := range ids {
			results = append(results, json.RawMessage(`{"id":"https://openalex.org/`+id+`","display_name":"`+id+`"}`))
		}
		payload, _ := json.Marshal(map[string]any{
			"meta":    map[string]int{"count": len(ids)},
			"results": results,
		})
		_, _ = w.Write(payload)
	})

	ids := make([]string, 0, 250)
	for i := range 250 {
		ids = append(ids, "W"+strings.Repeat("0", 1)+string(rune('A'+i%26))+strconv.Itoa(i))
	}
	papers, err := c.GetPaperBatch(context.Background(), ids, nil)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(papers) != len(ids) {
		t.Errorf("got %d papers, want %d", len(papers), len(ids))
	}
	if calls.Load() != 3 {
		t.Errorf("chunked calls = %d, want 3 (ceil(250/100))", calls.Load())
	}
}

func TestOpenAlexErrorMapping(t *testing.T) {
	t.Run("404 → ErrNotFound", func(t *testing.T) {
		c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		})
		_, err := c.GetPaper(context.Background(), "W999", nil)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("persistent 429 → ErrRateLimited", func(t *testing.T) {
		c, _ := newOpenAlexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
		})
		c.maxRetries = 1 // keep the test quick
		_, err := c.GetPaper(context.Background(), "W1", nil)
		if !errors.Is(err, ErrRateLimited) {
			t.Errorf("err = %v, want ErrRateLimited", err)
		}
	})
}

func TestOpenAlexMailtoAppended(t *testing.T) {
	var gotQuery atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery.Store(r.URL.Query())
		_, _ = w.Write([]byte(fixtureWork))
	}))
	t.Cleanup(srv.Close)
	c := NewOpenAlex(OpenAlexOptions{BaseURL: srv.URL, Mailto: "test@example.com", RPS: 1000, Burst: 1000})
	if _, err := c.GetPaper(context.Background(), "W42", nil); err != nil {
		t.Fatalf("GetPaper: %v", err)
	}
	got := gotQuery.Load().(url.Values).Get("mailto")
	if got != "test@example.com" {
		t.Errorf("mailto = %q", got)
	}
}

func TestReconstructAbstract(t *testing.T) {
	cases := []struct {
		name string
		idx  map[string][]int
		want string
	}{
		{"empty", nil, ""},
		{"single word", map[string][]int{"Hello": {0}}, "Hello"},
		{"words in order", map[string][]int{"Hello": {0}, "world": {1}}, "Hello world"},
		{"repeated word", map[string][]int{"the": {0, 2}, "cat": {1}, "mat": {3}}, "the cat the mat"},
		{"drops gaps", map[string][]int{"a": {0}, "c": {2}}, "a c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconstructAbstract(tc.idx); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeOpenAlexID(t *testing.T) {
	cases := map[string]string{
		"W123":                        "W123",
		"https://openalex.org/W123":   "W123",
		"10.1234/abc":                 "doi:10.1234/abc",
		"https://doi.org/10.1234/abc": "doi:10.1234/abc",
		"doi:10.1234/abc":             "doi:10.1234/abc",
		"pmid:99":                     "pmid:99",
		"":                            "",
		"  W7  ":                      "W7",
	}
	for in, want := range cases {
		if got := normalizeOpenAlexID(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripOpenAlexPrefix(t *testing.T) {
	if got := stripOpenAlexPrefix("https://openalex.org/W7"); got != "W7" {
		t.Errorf("strip URL: %q", got)
	}
	if got := stripOpenAlexPrefix("W7"); got != "W7" {
		t.Errorf("strip bare: %q", got)
	}
	if got := stripOpenAlexPrefix(""); got != "" {
		t.Errorf("strip empty: %q", got)
	}
}
