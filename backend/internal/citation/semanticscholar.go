package citation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultBaseURL = "https://api.semanticscholar.org/graph/v1"
	userAgent      = "better-connected-paper/0.1 (+https://github.com/saitenntaisei/better-connected-paper)"
)

// ErrNotFound is returned when S2 responds with 404.
var ErrNotFound = errors.New("semanticscholar: paper not found")

// ErrRateLimited wraps 429 responses after retries are exhausted.
var ErrRateLimited = errors.New("semanticscholar: rate limited")

// Client talks to Semantic Scholar's Academic Graph API v1.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	limiter    *rate.Limiter
	maxRetries int
}

// Options configure the Client.
type Options struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	// RPS caps outgoing requests. Zero picks a safe default:
	// 1 req/sec with API key, 1 req/3s without (stays under the shared pool).
	RPS        float64
	Burst      int
	MaxRetries int
}

// New builds a Client with sensible defaults.
func New(opts Options) *Client {
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.RPS == 0 {
		if opts.APIKey == "" {
			opts.RPS = 1.0 / 3.0
		} else {
			opts.RPS = 1.0
		}
	}
	if opts.Burst == 0 {
		opts.Burst = 2
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 3
	}
	return &Client{
		httpClient: opts.HTTPClient,
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		apiKey:     opts.APIKey,
		limiter:    rate.NewLimiter(rate.Limit(opts.RPS), opts.Burst),
		maxRetries: opts.MaxRetries,
	}
}

// Search finds papers matching query. Fields is a comma-joined subset of the S2 fields list.
func (c *Client) Search(ctx context.Context, query string, limit int, fields []string) (*SearchResponse, error) {
	if limit <= 0 {
		limit = 10
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("fields", strings.Join(fields, ","))

	var out SearchResponse
	if err := c.get(ctx, "/paper/search?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPaper fetches a single paper by id (supports DOI:, ARXIV:, S2 id, etc.).
func (c *Client) GetPaper(ctx context.Context, id string, fields []string) (*Paper, error) {
	q := url.Values{}
	q.Set("fields", strings.Join(fields, ","))
	var p Paper
	if err := c.get(ctx, "/paper/"+url.PathEscape(id)+"?"+q.Encode(), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// GetReferences returns papers cited by id.
func (c *Client) GetReferences(ctx context.Context, id string, limit int, fields []string) ([]Paper, error) {
	papers, err := c.listPapers(ctx, fmt.Sprintf("/paper/%s/references", url.PathEscape(id)), limit, fields, "citedPaper")
	if err != nil {
		return nil, err
	}
	return papers, nil
}

// GetCitations returns papers citing id.
func (c *Client) GetCitations(ctx context.Context, id string, limit int, fields []string) ([]Paper, error) {
	papers, err := c.listPapers(ctx, fmt.Sprintf("/paper/%s/citations", url.PathEscape(id)), limit, fields, "citingPaper")
	if err != nil {
		return nil, err
	}
	return papers, nil
}

// GetPaperBatch retrieves up to 500 papers in a single POST.
func (c *Client) GetPaperBatch(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := url.Values{}
	q.Set("fields", strings.Join(fields, ","))
	body, err := json.Marshal(map[string][]string{"ids": ids})
	if err != nil {
		return nil, err
	}

	var raw []json.RawMessage
	if err := c.do(ctx, http.MethodPost, "/paper/batch?"+q.Encode(), bytes.NewReader(body), &raw); err != nil {
		return nil, err
	}

	out := make([]Paper, 0, len(raw))
	for _, r := range raw {
		if len(r) == 0 || string(r) == "null" {
			continue
		}
		var p Paper
		if err := json.Unmarshal(r, &p); err != nil {
			return nil, fmt.Errorf("decode batch entry: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (c *Client) listPapers(ctx context.Context, path string, limit int, fields []string, wrapKey string) ([]Paper, error) {
	if limit <= 0 {
		limit = 100
	}
	perPage := min(limit, 100)

	nestedFields := make([]string, len(fields))
	for i, f := range fields {
		nestedFields[i] = wrapKey + "." + f
	}

	var out []Paper
	offset := 0
	for len(out) < limit {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(perPage))
		q.Set("offset", strconv.Itoa(offset))
		q.Set("fields", strings.Join(nestedFields, ","))

		var raw struct {
			Offset int                          `json:"offset"`
			Next   int                          `json:"next"`
			Data   []map[string]json.RawMessage `json:"data"`
		}
		if err := c.get(ctx, path+"?"+q.Encode(), &raw); err != nil {
			return nil, err
		}
		if len(raw.Data) == 0 {
			break
		}
		for _, entry := range raw.Data {
			body, ok := entry[wrapKey]
			if !ok {
				continue
			}
			var p Paper
			if err := json.Unmarshal(body, &p); err != nil {
				return nil, fmt.Errorf("decode %s: %w", wrapKey, err)
			}
			if p.PaperID == "" {
				continue
			}
			out = append(out, p)
			if len(out) >= limit {
				break
			}
		}
		if raw.Next == 0 {
			break
		}
		offset = raw.Next
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	var reqBody []byte
	if body != nil {
		b, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		reqBody = b
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}

		var r io.Reader
		if reqBody != nil {
			r = bytes.NewReader(reqBody)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")
		if reqBody != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.apiKey != "" {
			req.Header.Set("x-api-key", c.apiKey)
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
			lastErr = fmt.Errorf("semanticscholar: HTTP %d", resp.StatusCode)
			// S2 often returns Retry-After on 429 — honor it when present,
			// otherwise use exponential backoff. Clamp to 30s to avoid
			// stalling the whole request beyond the Vercel function budget.
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
			return fmt.Errorf("semanticscholar: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
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

func backoff(attempt int) time.Duration {
	base := 500 * time.Millisecond
	return min(base*(1<<attempt), 8*time.Second)
}

// parseRetryAfter accepts either seconds ("30") or an HTTP-date. Unknown input
// returns 0 so the caller falls back to its own backoff.
func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
