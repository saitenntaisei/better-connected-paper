package citation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"
)

func newTestAr5ivClient(t *testing.T, h http.Handler) *Ar5ivClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewAr5ivClient(Ar5ivOptions{
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	c.limiter = rate.NewLimiter(rate.Inf, 1)
	return c
}

func TestAr5ivGetReferencesExtractsArxivIDs(t *testing.T) {
	const page = `<html><body>
<ul class="ltx_biblist">
  <li id="bib.bib1" class="ltx_bibitem">Smith et al. Foo. arXiv:2502.13923, 2025.</li>
  <li id="bib.bib2" class="ltx_bibitem">Lee. Bar. arXiv: 2503.17434 .</li>
  <li id="bib.bib3" class="ltx_bibitem">Nakamura. Journal paper, no arxiv id.</li>
  <li id="bib.bib4" class="ltx_bibitem">Dup ref. arXiv:2502.13923, 2025.</li>
  <li id="bib.bib5" class="ltx_bibitem">Tanaka. Anchor link form. <a href="https://arxiv.org/abs/2410.24164">arXiv:2410.24164</a>.</li>
</ul>
</body></html>`
	mux := http.NewServeMux()
	mux.HandleFunc("/html/2511.14148", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("ar5iv client must send a User-Agent")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	})
	c := newTestAr5ivClient(t, mux)

	refs, err := c.GetReferences(context.Background(), "2511.14148")
	if err != nil {
		t.Fatalf("get refs: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("want 3 deduped arxiv refs (2502.13923, 2503.17434, 2410.24164), got %d: %+v", len(refs), refs)
	}
	got := map[string]bool{}
	for _, p := range refs {
		got[p.ExternalIDs["ArXiv"]] = true
	}
	for _, want := range []string{"2502.13923", "2503.17434", "2410.24164"} {
		if !got[want] {
			t.Errorf("missing %s in refs %v", want, got)
		}
	}
}

func TestAr5ivGetReferencesReturnsErrNotFoundOn404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/html/missing", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	c := newTestAr5ivClient(t, mux)

	if _, err := c.GetReferences(context.Background(), "missing"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestAr5ivGetReferencesEmptyOnEmptyArxivID(t *testing.T) {
	c := NewAr5ivClient(Ar5ivOptions{})
	refs, err := c.GetReferences(context.Background(), "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("empty id must short-circuit, got %d refs", len(refs))
	}
}
