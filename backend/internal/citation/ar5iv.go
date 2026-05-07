package citation

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultAr5ivBaseURL = "https://ar5iv.labs.arxiv.org"
	ar5ivUserAgent      = "better-connected-paper/0.1 (+https://github.com/saitenntaisei/better-connected-paper)"
)

// Ar5ivClient fetches paper bibliographies from ar5iv (the arxiv-labs LaTeX
// → HTML conversion). Used as a last-resort references fallback when
// neither OpenAlex nor S2 can supply refs for an arxiv preprint —
// publisher elision (AAAI / ICML / etc.) routinely zeroes out the
// references field on S2's API even when the underlying paper has them.
//
// HTML parsing is deliberately regex-based: the only data we extract is
// arxiv ids out of `<li class="ltx_bibitem">` blocks, which the existing
// DOI resolver translates into OpenAlex W-IDs via the synthesised
// 10.48550/arxiv.<id> form.
type Ar5ivClient struct {
	httpClient *http.Client
	baseURL    string
	limiter    *rate.Limiter
}

// Ar5ivOptions configure the Ar5ivClient.
type Ar5ivOptions struct {
	BaseURL    string
	HTTPClient *http.Client
	// RPS caps outgoing requests. ar5iv is a free arxiv-labs service —
	// keep it polite at 1 req/s by default.
	RPS   float64
	Burst int
}

// NewAr5ivClient builds an Ar5ivClient with sensible defaults.
func NewAr5ivClient(opts Ar5ivOptions) *Ar5ivClient {
	if opts.BaseURL == "" {
		opts.BaseURL = defaultAr5ivBaseURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.RPS == 0 {
		opts.RPS = 1.0
	}
	if opts.Burst == 0 {
		opts.Burst = 2
	}
	return &Ar5ivClient{
		httpClient: opts.HTTPClient,
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		limiter:    rate.NewLimiter(rate.Limit(opts.RPS), opts.Burst),
	}
}

var (
	ar5ivBibitemRegex = regexp.MustCompile(`(?is)<li[^>]*ltx_bibitem[^>]*>(.*?)</li>`)
	ar5ivArxivIDRegex = regexp.MustCompile(`(?i)arxiv[:\s]*(\d{4}\.\d{4,5})`)
)

// GetReferences returns deduped Papers carrying just the ArXiv externalId
// for every arxiv-cited paper found in arxivID's bibliography. Empty
// (no error) when ar5iv has no parseable bibitems for the paper.
func (c *Ar5ivClient) GetReferences(ctx context.Context, arxivID string) ([]Paper, error) {
	arxivID = strings.TrimSpace(arxivID)
	if arxivID == "" {
		return nil, nil
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/html/"+arxivID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ar5ivUserAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ar5iv: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseAr5ivBibitems(body), nil
}

// parseAr5ivBibitems extracts deduped arxiv IDs from ar5iv HTML. Exposed
// at package scope so the parser can be exercised without a HTTP server
// in unit tests.
func parseAr5ivBibitems(html []byte) []Paper {
	items := ar5ivBibitemRegex.FindAll(html, -1)
	seen := make(map[string]struct{}, len(items))
	out := make([]Paper, 0, len(items))
	for _, item := range items {
		for _, m := range ar5ivArxivIDRegex.FindAllSubmatch(item, -1) {
			if len(m) < 2 {
				continue
			}
			id := strings.ToLower(strings.TrimSpace(string(m[1])))
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, Paper{ExternalIDs: ExternalIDs{"ArXiv": id}})
		}
	}
	return out
}
