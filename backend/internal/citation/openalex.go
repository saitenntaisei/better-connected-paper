package citation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	openAlexBaseURL    = "https://api.openalex.org"
	openAlexUserAgent  = "better-connected-paper/0.1 (+https://github.com/saitenntaisei/better-connected-paper)"
	openAlexBatchChunk = 100
	// OpenAlex's per-page max is 200. Anything above that would paginate and
	// we deliberately surface a truncated fetch as "unknown" rather than
	// persisting a partial list — so 200 is the largest complete fetch we can
	// ever return. Papers with more than 200 citers (a handful of famous
	// preprints like pi_0) still get CitationsUnknown.
	openAlexCitesLimit = 200
	// openAlexCitesWorkers bounds goroutines enriching a batch with citations.
	// The rate limiter is the real throughput ceiling; this just caps in-flight sockets.
	openAlexCitesWorkers = 8
)

// openAlexSelectFields is the full payload we care about. OpenAlex bills
// nothing extra for more fields, and the payload stays small (~2KB/work),
// so we don't translate the caller's `fields` list — we just always ask
// for everything we might need.
const openAlexSelectFields = "id,display_name,abstract_inverted_index,publication_year,primary_location,authorships,cited_by_count,referenced_works_count,referenced_works,ids"

// OpenAlexClient talks to https://api.openalex.org/works. It satisfies the
// same contract as *Client (see graph.S2 and api.PaperClient), so it's a
// drop-in replacement for the Semantic Scholar client.
type OpenAlexClient struct {
	httpClient *http.Client
	baseURL    string
	mailto     string
	limiter    *rate.Limiter
	maxRetries int
}

// OpenAlexOptions configure the client.
type OpenAlexOptions struct {
	BaseURL    string
	Mailto     string // opt-in "polite pool" email
	HTTPClient *http.Client
	// RPS caps outgoing requests. Zero picks 10/s (OpenAlex's anonymous cap).
	RPS        float64
	Burst      int
	MaxRetries int
}

// NewOpenAlex builds a client with sensible defaults.
func NewOpenAlex(opts OpenAlexOptions) *OpenAlexClient {
	if opts.BaseURL == "" {
		opts.BaseURL = openAlexBaseURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.RPS == 0 {
		opts.RPS = 10
	}
	if opts.Burst == 0 {
		opts.Burst = 5
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 3
	}
	return &OpenAlexClient{
		httpClient: opts.HTTPClient,
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		mailto:     strings.TrimSpace(opts.Mailto),
		limiter:    rate.NewLimiter(rate.Limit(opts.RPS), opts.Burst),
		maxRetries: opts.MaxRetries,
	}
}

// Search maps /works?search=&per-page=.
func (c *OpenAlexClient) Search(ctx context.Context, query string, limit int, _ []string) (*SearchResponse, error) {
	if limit <= 0 {
		limit = 10
	}
	q := url.Values{}
	q.Set("search", query)
	q.Set("per-page", strconv.Itoa(limit))
	q.Set("select", openAlexSelectFields)

	var raw openAlexList
	if err := c.get(ctx, "/works?"+q.Encode(), &raw); err != nil {
		return nil, err
	}
	papers := make([]Paper, 0, len(raw.Results))
	for _, w := range raw.Results {
		papers = append(papers, toPaper(w))
	}
	return &SearchResponse{Total: raw.Meta.Count, Data: papers}, nil
}

// GetPaper fetches a single work by ID. Accepts OpenAlex W-IDs, "doi:..."
// prefixed DOIs, or bare DOIs starting with "10.". If the caller's fields
// list asks for citations.paperId, we fire a second request to populate
// Citations with up to openAlexCitesLimit IDs.
func (c *OpenAlexClient) GetPaper(ctx context.Context, id string, fields []string) (*Paper, error) {
	nid := normalizeOpenAlexID(id)
	if nid == "" {
		return nil, ErrNotFound
	}
	q := url.Values{}
	q.Set("select", openAlexSelectFields)

	var w openAlexWork
	if err := c.get(ctx, "/works/"+url.PathEscape(nid)+"?"+q.Encode(), &w); err != nil {
		return nil, err
	}
	p := toPaper(w)
	if requestsCitations(fields) && p.PaperID != "" {
		switch {
		case p.CitationCount == 0:
			// Genuinely zero — Citations stays empty and complete.
		case p.CitationCount > openAlexCitesLimit:
			// Would only ever see a truncated prefix; flag the unknown so
			// the cache layer won't persist an empty cites set as complete.
			p.CitationsUnknown = true
		default:
			cites, err := c.fetchCitesList(ctx, p.PaperID, openAlexCitesLimit)
			if err != nil || cites == nil {
				p.CitationsUnknown = true
			} else {
				p.Citations = cites
			}
		}
	}
	return &p, nil
}

// ResolveByDOI takes bare DOIs (no "doi:" prefix) and returns the
// corresponding OpenAlex Papers. Papers that OpenAlex can't match drop
// silently — the caller uses this to translate reference DOIs from
// OpenCitations into the W-ID space the graph builder operates in, and
// a missing paper just means one fewer edge in the final graph.
// The returned Paper carries ExternalIDs["DOI"] so batch callers can
// associate each resolved W-ID back to the input DOI that produced it.
func (c *OpenAlexClient) ResolveByDOI(ctx context.Context, dois []string) ([]Paper, error) {
	if len(dois) == 0 {
		return nil, nil
	}
	const chunkSize = 50
	out := make([]Paper, 0, len(dois))
	for start := 0; start < len(dois); start += chunkSize {
		end := min(start+chunkSize, len(dois))
		chunk := dois[start:end]
		filter := "doi:" + strings.Join(chunk, "|")
		q := url.Values{}
		q.Set("filter", filter)
		q.Set("per-page", strconv.Itoa(len(chunk)))
		q.Set("select", "id,ids")

		var raw openAlexList
		if err := c.get(ctx, "/works?"+q.Encode(), &raw); err != nil {
			return nil, err
		}
		for _, w := range raw.Results {
			pid := stripOpenAlexPrefix(w.ID)
			if pid == "" {
				continue
			}
			p := Paper{PaperID: pid}
			if w.Ids.DOI != "" {
				doi := strings.ToLower(strings.TrimPrefix(w.Ids.DOI, "https://doi.org/"))
				p.ExternalIDs = ExternalIDs{"DOI": doi}
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// GetPaperBatch fans the ID list out over /works?filter=openalex:W1|W2|...
// in chunks of openAlexBatchChunk. When the caller requests citations, each
// paper is enriched with up to openAlexCitesLimit cited-by IDs via a second
// pass of concurrent /works?filter=cites: queries. The rate limiter caps
// overall throughput, so the enrichment cost scales roughly len(ids)/RPS.
func (c *OpenAlexClient) GetPaperBatch(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]Paper, 0, len(ids))
	for start := 0; start < len(ids); start += openAlexBatchChunk {
		end := min(start+openAlexBatchChunk, len(ids))
		chunk := ids[start:end]
		filter := "openalex:" + strings.Join(chunk, "|")
		q := url.Values{}
		q.Set("filter", filter)
		q.Set("per-page", strconv.Itoa(len(chunk)))
		q.Set("select", openAlexSelectFields)

		var raw openAlexList
		if err := c.get(ctx, "/works?"+q.Encode(), &raw); err != nil {
			return nil, err
		}
		for _, w := range raw.Results {
			out = append(out, toPaper(w))
		}
	}
	if requestsCitations(fields) {
		c.enrichCitesConcurrent(ctx, out)
	}
	return out, nil
}

// enrichCitesConcurrent populates Citations in-place for each paper that has
// a non-empty PaperID. A per-paper failure is silently skipped — the Builder
// already tolerates missing cites by falling back to refs-only scoring — but
// context cancellation aborts remaining work.
func (c *OpenAlexClient) enrichCitesConcurrent(ctx context.Context, papers []Paper) {
	jobs := make(chan int, len(papers))
	var wg sync.WaitGroup
	for range openAlexCitesWorkers {
		wg.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				if papers[i].PaperID == "" {
					continue
				}
				if papers[i].CitationCount == 0 {
					// Genuinely zero — leave Citations empty, mark complete.
					continue
				}
				// Above the per-page cap we'd only see a truncated prefix,
				// which would corrupt scoring and cache if persisted. Flag
				// these as unknown so the persist layer skips them.
				if papers[i].CitationCount > openAlexCitesLimit {
					papers[i].CitationsUnknown = true
					continue
				}
				cites, err := c.fetchCitesList(ctx, papers[i].PaperID, openAlexCitesLimit)
				if err != nil || cites == nil {
					papers[i].CitationsUnknown = true
					continue
				}
				papers[i].Citations = cites
			}
		})
	}
	for i := range papers {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	// Context cancellation would leave never-processed papers with no
	// Citations and no CitationsUnknown flag, which the persist layer would
	// then cache as "zero citers" — corrupting future scoring. Flag every
	// non-trivial paper we didn't successfully populate as unknown so the
	// persist layer skips it.
	if ctx.Err() != nil {
		for i := range papers {
			if papers[i].PaperID == "" || papers[i].CitationCount == 0 {
				continue
			}
			if papers[i].CitationsUnknown || len(papers[i].Citations) > 0 {
				continue
			}
			papers[i].CitationsUnknown = true
		}
	}
}

// fetchCitesList returns papers citing `id`, populated with only the OpenAlex
// ID. If the citing set is larger than limit, returns nil — callers rely on
// the list being complete (anything else corrupts cache + scoring), so a
// truncated fetch is surfaced as "no data".
func (c *OpenAlexClient) fetchCitesList(ctx context.Context, id string, limit int) ([]Paper, error) {
	q := url.Values{}
	q.Set("filter", "cites:"+id)
	q.Set("per-page", strconv.Itoa(limit))
	q.Set("select", "id")

	var raw openAlexList
	if err := c.get(ctx, "/works?"+q.Encode(), &raw); err != nil {
		return nil, err
	}
	if raw.Meta.Count > len(raw.Results) {
		return nil, nil
	}
	out := make([]Paper, 0, len(raw.Results))
	for _, w := range raw.Results {
		pid := stripOpenAlexPrefix(w.ID)
		if pid != "" {
			out = append(out, Paper{PaperID: pid})
		}
	}
	return out, nil
}

func (c *OpenAlexClient) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *OpenAlexClient) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	// OpenAlex's "polite pool" is opt-in via mailto in the query string.
	// Append it once per call rather than rewriting URLs everywhere.
	if c.mailto != "" {
		sep := "&"
		if !strings.Contains(path, "?") {
			sep = "?"
		}
		path = path + sep + "mailto=" + url.QueryEscape(c.mailto)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", openAlexUserAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if !sleepCtx(ctx, backoff(attempt)) {
				return ctx.Err()
			}
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			defer resp.Body.Close()
			return json.NewDecoder(resp.Body).Decode(out)
		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return ErrNotFound
		case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			lastErr = fmt.Errorf("openalex: HTTP %d", resp.StatusCode)
			wait := backoff(attempt)
			if retryAfter > 0 && retryAfter > wait {
				wait = retryAfter
			}
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			if !sleepCtx(ctx, wait) {
				return ctx.Err()
			}
		default:
			payload, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("openalex: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
		}
	}
	if errors.Is(lastErr, ErrRateLimited) || strings.Contains(fmt.Sprint(lastErr), "429") {
		return ErrRateLimited
	}
	if lastErr != nil {
		return lastErr
	}
	return ErrRateLimited
}

// --- JSON shapes ---

type openAlexWork struct {
	ID                    string               `json:"id"`
	DisplayName           string               `json:"display_name"`
	PublicationYear       int                  `json:"publication_year"`
	CitedByCount          int                  `json:"cited_by_count"`
	ReferencedWorksCount  int                  `json:"referenced_works_count"`
	ReferencedWorks       []string             `json:"referenced_works"`
	AbstractInvertedIndex map[string][]int     `json:"abstract_inverted_index"`
	PrimaryLocation       *openAlexLocation    `json:"primary_location"`
	Authorships           []openAlexAuthorship `json:"authorships"`
	Ids                   openAlexIds          `json:"ids"`
}

type openAlexLocation struct {
	Source *openAlexSource `json:"source"`
}

type openAlexSource struct {
	DisplayName string `json:"display_name"`
}

type openAlexAuthorship struct {
	Author openAlexAuthor `json:"author"`
}

type openAlexAuthor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type openAlexIds struct {
	OpenAlex string `json:"openalex"`
	DOI      string `json:"doi"`
	PMID     string `json:"pmid"`
	MAG      string `json:"mag"`
}

type openAlexList struct {
	Meta struct {
		Count int `json:"count"`
	} `json:"meta"`
	Results []openAlexWork `json:"results"`
}

// --- Helpers ---

// toPaper translates an OpenAlex work into the canonical citation.Paper.
func toPaper(w openAlexWork) Paper {
	refs := make([]Paper, 0, len(w.ReferencedWorks))
	for _, ref := range w.ReferencedWorks {
		id := stripOpenAlexPrefix(ref)
		if id != "" {
			refs = append(refs, Paper{PaperID: id})
		}
	}

	authors := make([]Author, 0, len(w.Authorships))
	for _, a := range w.Authorships {
		if a.Author.DisplayName == "" {
			continue
		}
		authors = append(authors, Author{
			AuthorID: stripOpenAlexPrefix(a.Author.ID),
			Name:     a.Author.DisplayName,
		})
	}

	venue := ""
	if w.PrimaryLocation != nil && w.PrimaryLocation.Source != nil {
		venue = w.PrimaryLocation.Source.DisplayName
	}

	externalIDs := make(ExternalIDs)
	if w.Ids.DOI != "" {
		externalIDs["DOI"] = strings.TrimPrefix(w.Ids.DOI, "https://doi.org/")
	}
	if w.Ids.PMID != "" {
		externalIDs["PubMed"] = strings.TrimPrefix(w.Ids.PMID, "https://pubmed.ncbi.nlm.nih.gov/")
	}
	if w.Ids.MAG != "" {
		externalIDs["MAG"] = w.Ids.MAG
	}

	paperID := stripOpenAlexPrefix(w.ID)

	// Prefer a DOI resolver link so clicking the URL lands on the paper's
	// real home page. Fallback to the OpenAlex record URL.
	urlStr := ""
	if doi, ok := externalIDs["DOI"]; ok && doi != "" {
		urlStr = "https://doi.org/" + doi
	} else if w.ID != "" {
		urlStr = w.ID
	}

	return Paper{
		PaperID:        paperID,
		Title:          w.DisplayName,
		Abstract:       reconstructAbstract(w.AbstractInvertedIndex),
		Year:           w.PublicationYear,
		Venue:          venue,
		Authors:        authors,
		CitationCount:  w.CitedByCount,
		ReferenceCount: w.ReferencedWorksCount,
		ExternalIDs:    externalIDs,
		URL:            urlStr,
		References:     refs,
	}
}

// reconstructAbstract turns OpenAlex's inverted-index encoding back into
// readable text. The index is {word: [positions, ...]}; we allocate a slot
// per position, drop in each word, and join with spaces.
func reconstructAbstract(idx map[string][]int) string {
	if len(idx) == 0 {
		return ""
	}
	maxPos := -1
	for _, positions := range idx {
		for _, p := range positions {
			if p > maxPos {
				maxPos = p
			}
		}
	}
	if maxPos < 0 {
		return ""
	}
	words := make([]string, maxPos+1)
	for w, positions := range idx {
		for _, p := range positions {
			if p >= 0 && p <= maxPos {
				words[p] = w
			}
		}
	}
	nonEmpty := words[:0]
	for _, w := range words {
		if w != "" {
			nonEmpty = append(nonEmpty, w)
		}
	}
	return strings.Join(nonEmpty, " ")
}

// stripOpenAlexPrefix turns "https://openalex.org/W123" into "W123".
// Safe for empty input and for values that are already bare IDs.
func stripOpenAlexPrefix(s string) string {
	return strings.TrimPrefix(s, "https://openalex.org/")
}

// normalizeOpenAlexID accepts the grab-bag of ID forms the frontend and
// callers might send (OpenAlex URLs, bare W-IDs, DOIs with or without the
// https prefix, prefixed doi:..., pmid:...) and returns the form
// /works/{id} expects.
func normalizeOpenAlexID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(id, "https://openalex.org/"):
		return strings.TrimPrefix(id, "https://openalex.org/")
	case strings.HasPrefix(id, "https://doi.org/"):
		return "doi:" + strings.TrimPrefix(id, "https://doi.org/")
	case strings.HasPrefix(id, "10."):
		return "doi:" + id
	}
	return id
}

// requestsCitations returns true if the caller asked for the S2-shape
// `citations.*` fields — the Builder's seedFields does. We use this as
// the trigger to fire the extra /works?filter=cites:... request.
func requestsCitations(fields []string) bool {
	for _, f := range fields {
		if strings.HasPrefix(f, "citations") {
			return true
		}
	}
	return false
}
