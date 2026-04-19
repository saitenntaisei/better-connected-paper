package citation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	openCitationsBaseURL = "https://opencitations.net/index/api/v2"
	openCitationsUA      = "better-connected-paper/0.1 (+https://github.com/saitenntaisei/better-connected-paper)"
)

// DOIResolver turns bare DOIs (no "doi:" prefix) into Papers in the primary
// ID space (typically OpenAlex W-IDs). OpenCitationsClient uses it to keep
// the graph in a single ID space: its raw /references and /citations rows
// come back keyed by DOI, which we translate up-front so downstream callers
// never see a mixed-provider edge set.
type DOIResolver func(ctx context.Context, dois []string) ([]Paper, error)

// OpenCitationsClient fetches refs/cites from OpenCitations v2 (the unified
// Meta/COCI index). Unlike OpenAlex, it indexes arxiv preprints by DOI, so
// it's a better fit for the hybrid secondary slot than Semantic Scholar,
// which has a hostile anonymous rate limit.
//
// The Paper returned by GetPaper carries only References/Citations, not
// title/year/authors — those stay with the primary provider and are merged
// by HybridClient.mergeFromSecondary. PaperID is intentionally empty so the
// hybrid merge doesn't try to set MergedFromID (the refs/cites returned
// here are already in primary space thanks to Resolver, so no aliasing is
// needed).
type OpenCitationsClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
	mailto     string
	resolver   DOIResolver
	limiter    *rate.Limiter
	maxRetries int
}

// OpenCitationsOptions configure the client.
type OpenCitationsOptions struct {
	BaseURL    string
	Token      string // optional — granted by OpenCitations for higher rate limits
	Mailto     string // opt-in contact email, appended as ?mailto=
	HTTPClient *http.Client
	Resolver   DOIResolver // required — DOIs → Papers in primary space
	// RPS caps outgoing requests. Zero picks 5/s, well under the documented
	// anonymous ceiling and leaving headroom for bursts.
	RPS        float64
	Burst      int
	MaxRetries int
}

// NewOpenCitations builds a client with sensible defaults.
func NewOpenCitations(opts OpenCitationsOptions) *OpenCitationsClient {
	if opts.BaseURL == "" {
		opts.BaseURL = openCitationsBaseURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.RPS == 0 {
		opts.RPS = 5
	}
	if opts.Burst == 0 {
		opts.Burst = 2
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 3
	}
	return &OpenCitationsClient{
		httpClient: opts.HTTPClient,
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		token:      strings.TrimSpace(opts.Token),
		mailto:     strings.TrimSpace(opts.Mailto),
		resolver:   opts.Resolver,
		limiter:    rate.NewLimiter(rate.Limit(opts.RPS), opts.Burst),
		maxRetries: opts.MaxRetries,
	}
}

// Search is a no-op: OpenCitations has no title-search endpoint. An empty
// response keeps the HybridClient title-fallback path safe (it just logs
// "no secondary match" and returns the primary Paper as-is).
func (c *OpenCitationsClient) Search(_ context.Context, _ string, _ int, _ []string) (*SearchResponse, error) {
	return &SearchResponse{Data: nil}, nil
}

// GetPaper populates References and/or Citations for `id`. Accepts DOIs
// (bare, "doi:" prefixed, or https://doi.org URLs) and OpenAlex W-IDs.
// Returns ErrNotFound if the id shape isn't one OpenCitations can look up.
func (c *OpenCitationsClient) GetPaper(ctx context.Context, id string, fields []string) (*Paper, error) {
	ocID := toOpenCitationsID(id)
	if ocID == "" {
		return nil, ErrNotFound
	}

	p := Paper{}

	if requestsReferences(fields) {
		refs, err := c.fetchRelation(ctx, "references", ocID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		p.References = refs
		p.ReferenceCount = len(refs)
	}
	if requestsCitations(fields) {
		cites, err := c.fetchRelation(ctx, "citations", ocID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if cites == nil {
			p.CitationsUnknown = true
		} else {
			p.Citations = cites
		}
	}

	return &p, nil
}

// GetPaperBatch exists for PaperProvider completeness. HybridClient only
// routes IDs here when isSecondaryID matches (40-char hex, S2 shape), which
// OpenCitations never sees — but we implement it sequentially just in case
// a caller wires it directly.
func (c *OpenCitationsClient) GetPaperBatch(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]Paper, 0, len(ids))
	for _, id := range ids {
		p, err := c.GetPaper(ctx, id, fields)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// fetchRelation hits /references/{id} or /citations/{id}, extracts DOIs out
// of the multi-scheme cited/citing id field, deduplicates, and resolves
// them into primary-space Papers. Returns (nil, nil) for "no data" and
// (nil, ErrNotFound) when OpenCitations doesn't know the identifier at all.
func (c *OpenCitationsClient) fetchRelation(ctx context.Context, kind, ocID string) ([]Paper, error) {
	var rows []openCitationsRow
	path := "/" + kind + "/" + ocID
	if err := c.get(ctx, path, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	dois := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		field := r.Cited
		if kind == "citations" {
			field = r.Citing
		}
		doi := extractDOI(field)
		if doi == "" {
			continue
		}
		if _, dup := seen[doi]; dup {
			continue
		}
		seen[doi] = struct{}{}
		dois = append(dois, doi)
	}
	if len(dois) == 0 || c.resolver == nil {
		return nil, nil
	}
	return c.resolver(ctx, dois)
}

// openCitationsRow is one element of the /references or /citations array.
// The cited/citing fields are space-separated multi-scheme identifier
// strings like "doi:10.1234/abc pmid:99999 openalex:W123".
type openCitationsRow struct {
	Cited  string `json:"cited"`
	Citing string `json:"citing"`
}

// toOpenCitationsID normalizes caller-supplied IDs into the form v2 expects
// on its path segment (doi:..., openalex:...). Returns "" for shapes it
// doesn't support (e.g., arxiv:..., which OpenCitations only indexes by
// the paper's journal DOI, not the arxiv identifier).
func toOpenCitationsID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	lower := strings.ToLower(id)
	switch {
	case strings.HasPrefix(lower, "doi:"):
		return "doi:" + id[4:]
	case strings.HasPrefix(id, "10."):
		return "doi:" + id
	case strings.HasPrefix(id, "https://doi.org/"):
		return "doi:" + strings.TrimPrefix(id, "https://doi.org/")
	case strings.HasPrefix(lower, "openalex:"):
		return "openalex:" + id[9:]
	case strings.HasPrefix(id, "https://openalex.org/"):
		return "openalex:" + strings.TrimPrefix(id, "https://openalex.org/")
	case len(id) > 1 && id[0] == 'W':
		return "openalex:" + id
	}
	return ""
}

// extractDOI pulls the first "doi:..." token out of a space-separated
// multi-scheme id string and returns the bare DOI.
func extractDOI(s string) string {
	for tok := range strings.FieldsSeq(s) {
		if rest, ok := strings.CutPrefix(tok, "doi:"); ok {
			return rest
		}
	}
	return ""
}

func (c *OpenCitationsClient) get(ctx context.Context, path string, out any) error {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}

		u := c.baseURL + path
		if c.mailto != "" {
			sep := "&"
			if !strings.Contains(u, "?") {
				sep = "?"
			}
			u = u + sep + "mailto=" + url.QueryEscape(c.mailto)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", openCitationsUA)
		req.Header.Set("Accept", "application/json")
		if c.token != "" {
			req.Header.Set("authorization", c.token)
		}

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
			lastErr = fmt.Errorf("opencitations: HTTP %d", resp.StatusCode)
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
			return fmt.Errorf("opencitations: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return ErrRateLimited
}
