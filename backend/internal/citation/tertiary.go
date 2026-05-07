package citation

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
)

// refsBudgetKey scopes the per-build paginated-refs fallback budget so a
// single Builder.Build can fan out across firstHop / bridges / full
// fetches and still hit a hard total cap. Without this, a chunked batch
// (BatchSize 100, MaxFirstHop 300) could fire the fallback in ~6
// independent batches and stack ~60 s of /references calls onto the
// 60 s Vercel function cap.
type refsBudgetKey struct{}

type refsBudget struct {
	remaining int64
}

// WithRefsBackfillBudget caps how many paginated /references fallback
// calls ResolvingTertiary may issue for any work rooted at ctx. n ≤ 0
// disables the cap (unbounded). The budget is shared across goroutines
// via atomic decrement, so concurrent batches drawing from the same ctx
// can't double-spend.
func WithRefsBackfillBudget(ctx context.Context, n int) context.Context {
	if n <= 0 {
		return ctx
	}
	b := &refsBudget{remaining: int64(n)}
	return context.WithValue(ctx, refsBudgetKey{}, b)
}

// tryConsumeRefsBudget returns true if the caller may make one paginated
// /references call. Falls back to "allow" when no budget is attached so
// non-Builder callers (CLI tools, tests) keep their existing behaviour.
func tryConsumeRefsBudget(ctx context.Context) bool {
	v := ctx.Value(refsBudgetKey{})
	b, ok := v.(*refsBudget)
	if !ok || b == nil {
		return true
	}
	return atomic.AddInt64(&b.remaining, -1) >= 0
}

// ResolvingTertiary wraps an inner PaperProvider (typically a Semantic
// Scholar client) so its refs/cites come back in the primary's W-ID space
// rather than the inner provider's native hex paperId space. The hybrid
// graph builder's batch router assumes refs stay on one ID space; without
// this adapter, S2 hex refs would get shipped to OpenCitations (which
// can't resolve them) and silently drop.
//
// The adapter:
//  1. Enriches the fields list so the inner provider also returns
//     externalIds on each ref/cite.
//  2. Collects the DOIs from those externalIds and calls the injected
//     Resolver (OpenAlex in practice) to translate them into W-IDs.
//  3. Replaces refs/cites with minimal Papers carrying only the resolved
//     W-IDs — the builder only needs PaperID for ranking.
//  4. Clears the outer Paper's PaperID so HybridClient.mergeFromSecondary
//     doesn't set MergedFromID to an S2 hex (the graph already lives in
//     primary space thanks to the translation above).
type ResolvingTertiary struct {
	Inner    PaperProvider
	Resolver DOIResolver
	Logger   *slog.Logger
	// CiterSupplementLimit caps extra citers fetched via paginated lookup
	// when the inline response hits S2's 1000-item cap. Inline and paginated
	// endpoints use different orderings on S2, so offsetting past 1000
	// surfaces a disjoint band — where recent preprints (e.g. the 2024
	// robotics cluster that cites Octo) tend to cluster. 0 disables.
	CiterSupplementLimit int
}

// citerLister lets the tertiary supplement the inline 1000-item cap without
// pulling a full *Client dependency. *Client satisfies it; tests can stub it.
type citerLister interface {
	GetCitationsFrom(ctx context.Context, id string, offset, limit int, fields []string) ([]Paper, error)
}

// referenceLister lets the tertiary fall back to S2's paginated
// /paper/{id}/references endpoint when the inline `references` field
// returned empty — a common shape for recent arxiv preprints whose
// metadata extraction has not finished propagating to the inline path.
type referenceLister interface {
	GetReferences(ctx context.Context, id string, limit int, fields []string) ([]Paper, error)
}

// Search passes through; hybrid uses its result only to grab a hit.PaperID
// which then flows back into GetPaper, where translation happens.
func (r *ResolvingTertiary) Search(ctx context.Context, query string, limit int, fields []string) (*SearchResponse, error) {
	if r.Inner == nil {
		return &SearchResponse{}, nil
	}
	return r.Inner.Search(ctx, query, limit, fields)
}

// GetPaper calls inner with enriched fields, then swaps refs/cites in-place
// with DOI-resolved primary-space Papers.
func (r *ResolvingTertiary) GetPaper(ctx context.Context, id string, fields []string) (*Paper, error) {
	if r.Inner == nil {
		return nil, ErrNotFound
	}
	p, err := r.Inner.GetPaper(ctx, id, enrichTertiaryFields(fields))
	if err != nil || p == nil {
		return p, err
	}
	r.supplementCiters(ctx, id, p)
	r.supplementRefsViaPagination(ctx, id, p, fields)
	if refs := r.translate(ctx, p.References); refs != nil {
		p.References = refs
		if p.ReferenceCount < len(refs) {
			p.ReferenceCount = len(refs)
		}
	} else {
		p.References = nil
	}
	if cites := r.translate(ctx, p.Citations); cites != nil {
		p.Citations = cites
	} else {
		p.Citations = nil
	}
	p.PaperID = ""
	return p, nil
}

// supplementCiters merges paginated citers (offset ≥ 1000) into the inline
// response when S2 truncated at its 1000-item cap. The two endpoints sort
// citers differently, so the paginated tail reliably contains papers the
// inline response dropped — most visibly the recent-preprint band that makes
// or breaks the Prior Works cluster in our graph builds.
func (r *ResolvingTertiary) supplementCiters(ctx context.Context, id string, p *Paper) {
	if r.CiterSupplementLimit <= 0 || p == nil {
		return
	}
	const inlineCap = 1000
	if len(p.Citations) < inlineCap {
		return
	}
	if p.CitationCount > 0 && p.CitationCount <= inlineCap {
		return
	}
	lister, ok := r.Inner.(citerLister)
	if !ok {
		return
	}
	extras, err := lister.GetCitationsFrom(ctx, id, inlineCap, r.CiterSupplementLimit, []string{"paperId", "externalIds"})
	if err != nil {
		if r.Logger != nil {
			r.Logger.Warn("tertiary: citer supplement failed", "id", id, "err", err)
		}
		return
	}
	if len(extras) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(p.Citations)+len(extras))
	for i := range p.Citations {
		if pid := p.Citations[i].PaperID; pid != "" {
			seen[pid] = struct{}{}
		}
	}
	added := 0
	for i := range extras {
		pid := extras[i].PaperID
		if pid == "" {
			continue
		}
		if _, dup := seen[pid]; dup {
			continue
		}
		seen[pid] = struct{}{}
		p.Citations = append(p.Citations, extras[i])
		added++
	}
	if r.Logger != nil && added > 0 {
		r.Logger.Info("tertiary: citer supplement", "id", id, "inline", inlineCap, "added", added, "total", len(p.Citations))
	}
}

// GetPaperBatch fetches a batch of papers (IDs may be DOI:/ARXIV:/hex) via
// the inner provider, then replaces each paper's hex refs with W-IDs using
// the DOI resolver. Unlike GetPaper this does NOT supplement cites — cites
// are expensive to enrich per-paper and the Builder's first-hop path scores
// candidates off refs and the seed's citers, so skipping cites here keeps
// the batch cost bounded. Top-level ExternalIDs is preserved so callers can
// match returned papers back to their input DOIs.
//
// Papers whose inline refs came back empty get a per-paper paginated
// /references fallback (same shape as GetPaper). Calls draw from the
// per-build budget attached to ctx via WithRefsBackfillBudget, so a
// chunked first-hop can't fire 6 × budget calls in series and time out
// the build on Vercel.
func (r *ResolvingTertiary) GetPaperBatch(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
	if r.Inner == nil {
		return nil, nil
	}
	papers, err := r.Inner.GetPaperBatch(ctx, ids, enrichTertiaryFields(fields))
	if err != nil || len(papers) == 0 {
		return papers, err
	}
	if requestsReferences(fields) {
		spent, skipped := 0, 0
		for i := range papers {
			if len(papers[i].References) > 0 {
				continue
			}
			id := papers[i].PaperID
			if id == "" {
				continue
			}
			if !tryConsumeRefsBudget(ctx) {
				skipped++
				continue
			}
			r.supplementRefsViaPagination(ctx, id, &papers[i], fields)
			spent++
		}
		if skipped > 0 && r.Logger != nil {
			r.Logger.Info("tertiary: paginated refs fallback budget hit", "spent", spent, "skipped", skipped)
		}
	}
	r.translateRefsBatch(ctx, papers)
	for i := range papers {
		papers[i].Citations = nil
	}
	return papers, nil
}

// translateRefsBatch dedupes DOIs across every paper's refs, calls the
// resolver exactly once, and swaps each paper's hex refs with the W-ID
// versions. Papers with no resolvable refs come back with References=nil.
// This is O(N papers + D unique DOIs) regardless of paper count — the key
// reason we amortize the resolver call across the whole batch.
func (r *ResolvingTertiary) translateRefsBatch(ctx context.Context, papers []Paper) {
	if r.Resolver == nil {
		for i := range papers {
			papers[i].References = nil
		}
		return
	}
	doiSet := make(map[string]struct{})
	for i := range papers {
		for j := range papers[i].References {
			d := canonicalDOIFromExternalIDs(papers[i].References[j].ExternalIDs)
			if d == "" {
				continue
			}
			doiSet[d] = struct{}{}
		}
	}
	if len(doiSet) == 0 {
		for i := range papers {
			papers[i].References = nil
		}
		return
	}
	dois := make([]string, 0, len(doiSet))
	for d := range doiSet {
		dois = append(dois, d)
	}
	resolved, err := r.Resolver(ctx, dois)
	if err != nil {
		if r.Logger != nil {
			r.Logger.Warn("tertiary: batch DOI resolver failed", "err", err, "count", len(dois))
		}
		for i := range papers {
			papers[i].References = nil
		}
		return
	}
	doiToWID := make(map[string]string, len(resolved))
	for _, rp := range resolved {
		d := strings.ToLower(strings.TrimSpace(rp.ExternalIDs["DOI"]))
		if d == "" || rp.PaperID == "" {
			continue
		}
		doiToWID[d] = rp.PaperID
	}
	for i := range papers {
		if len(papers[i].References) == 0 {
			continue
		}
		translated := make([]Paper, 0, len(papers[i].References))
		seen := make(map[string]struct{}, len(papers[i].References))
		for _, ref := range papers[i].References {
			d := canonicalDOIFromExternalIDs(ref.ExternalIDs)
			if d == "" {
				continue
			}
			wid, ok := doiToWID[d]
			if !ok {
				continue
			}
			if _, dup := seen[wid]; dup {
				continue
			}
			seen[wid] = struct{}{}
			translated = append(translated, Paper{PaperID: wid})
		}
		if len(translated) == 0 {
			papers[i].References = nil
			continue
		}
		papers[i].References = translated
		if papers[i].ReferenceCount < len(translated) {
			papers[i].ReferenceCount = len(translated)
		}
	}
}

// Recommend forwards to the inner provider's recommendations endpoint
// (typically S2 /recommendations/v1/papers/forpaper/{id}) and translates
// each rec into a primary-space W-ID via the DOI resolver. When a rec
// only carries an ArXiv id, the canonical 10.48550/arxiv.<id> DOI is
// synthesised so OpenAlex can still resolve it — most recent preprints
// ship with ArXiv-only externalIds.
//
// Returns (nil, nil) if the inner provider doesn't implement Recommender;
// callers treat that as "no recommendations available" and continue.
func (r *ResolvingTertiary) Recommend(ctx context.Context, id string, limit int, fields []string) ([]Paper, error) {
	rec, ok := r.Inner.(Recommender)
	if !ok {
		return nil, nil
	}
	enriched := enrichRecommendFields(fields)
	raw, err := rec.Recommend(ctx, id, limit, enriched)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || r.Resolver == nil {
		return nil, nil
	}
	dois := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, p := range raw {
		d := canonicalDOIFromExternalIDs(p.ExternalIDs)
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		dois = append(dois, d)
	}
	if len(dois) == 0 {
		return nil, nil
	}
	resolved, err := r.Resolver(ctx, dois)
	if err != nil {
		if r.Logger != nil {
			r.Logger.Warn("tertiary: rec resolver failed", "err", err, "count", len(dois))
		}
		return nil, err
	}
	return resolved, nil
}

// supplementRefsViaPagination fills p.References from S2's paginated
// /paper/{id}/references endpoint when the inline response came back
// empty but the caller asked for refs. The inline `references` field is
// often blank for recent arxiv preprints (AsyncVLA's recs are the
// canonical case) while the paginated endpoint still returns 30-100
// citedPaper entries — without this fallback, biblio coupling among
// the recs cluster collapses to 0 and the cite arrows go missing.
func (r *ResolvingTertiary) supplementRefsViaPagination(ctx context.Context, id string, p *Paper, fields []string) {
	if p == nil || len(p.References) > 0 || !requestsReferences(fields) {
		return
	}
	lister, ok := r.Inner.(referenceLister)
	if !ok {
		return
	}
	refs, err := lister.GetReferences(ctx, id, 200, []string{"paperId", "externalIds"})
	if err != nil {
		if r.Logger != nil {
			r.Logger.Warn("tertiary: paginated refs supplement failed", "id", id, "err", err)
		}
		return
	}
	if len(refs) == 0 {
		// No log: S2 routinely returns 0 for new arxiv preprints whose
		// venues (AAAI etc.) have elided the references field via
		// publisher embargo. Logging every miss would dominate log volume
		// for sparse-seed builds.
		return
	}
	p.References = refs
	if p.ReferenceCount < len(refs) {
		p.ReferenceCount = len(refs)
	}
	if r.Logger != nil {
		r.Logger.Info("tertiary: paginated refs supplement", "id", id, "added", len(refs))
	}
}

// EmbeddingsByExternalID delegates to the inner provider when it implements
// Embedder (the S2 client does). The tertiary doesn't translate IDs here:
// callers key embeddings by DOI directly, so no W-ID resolution is needed.
// Returns (nil, nil) if the inner can't supply embeddings — callers treat
// that as "no embedding-similarity edges for this build".
func (r *ResolvingTertiary) EmbeddingsByExternalID(ctx context.Context, ids []string) (map[string][]float32, error) {
	if e, ok := r.Inner.(Embedder); ok {
		return e.EmbeddingsByExternalID(ctx, ids)
	}
	return nil, nil
}

// canonicalDOIFromExternalIDs prefers an explicit DOI but synthesises the
// arxiv-DOI form (10.48550/arxiv.<id>) when only an ArXiv id is present —
// modern arxiv preprints commonly ship that way and OpenAlex resolves the
// arxiv-DOI cleanly.
func canonicalDOIFromExternalIDs(ext ExternalIDs) string {
	if d := strings.ToLower(strings.TrimSpace(ext["DOI"])); d != "" {
		return d
	}
	if a := strings.TrimSpace(ext["ArXiv"]); a != "" {
		return "10.48550/arxiv." + strings.ToLower(a)
	}
	return ""
}

// enrichRecommendFields ensures the inner Recommend response carries the
// externalIds we need to resolve DOIs/ArXiv ids back into W-IDs. The
// caller's own field requests are preserved.
func enrichRecommendFields(fields []string) []string {
	hasPaperID, hasExt := false, false
	for _, f := range fields {
		switch f {
		case "paperId":
			hasPaperID = true
		case "externalIds":
			hasExt = true
		}
	}
	out := make([]string, 0, len(fields)+2)
	out = append(out, fields...)
	if !hasPaperID {
		out = append(out, "paperId")
	}
	if !hasExt {
		out = append(out, "externalIds")
	}
	return out
}

// translate collects DOIs from the items' externalIds and resolves them
// to W-ID Papers via the injected resolver. Arxiv-only items get a
// synthesised 10.48550/arxiv.<id> DOI so the paginated /references
// fallback (which surfaces arxiv-only entries from S2) doesn't lose
// them. Items with neither identifier are dropped. Returns nil when
// nothing resolved (caller treats nil as "clear the field").
func (r *ResolvingTertiary) translate(ctx context.Context, items []Paper) []Paper {
	if len(items) == 0 || r.Resolver == nil {
		return nil
	}
	dois := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for i := range items {
		d := canonicalDOIFromExternalIDs(items[i].ExternalIDs)
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		dois = append(dois, d)
	}
	if len(dois) == 0 {
		return nil
	}
	resolved, err := r.Resolver(ctx, dois)
	if err != nil {
		if r.Logger != nil {
			r.Logger.Warn("tertiary: DOI resolver failed", "err", err, "count", len(dois))
		}
		return nil
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

// enrichTertiaryFields adds references.externalIds / citations.externalIds
// whenever the caller asked for refs or cites, so the inner provider
// returns DOIs that translate() can feed into the resolver. Existing
// entries are preserved untouched.
func enrichTertiaryFields(fields []string) []string {
	wantRefs, wantCites := false, false
	hasRefExt, hasCiteExt := false, false
	for _, f := range fields {
		switch f {
		case "references.externalIds":
			hasRefExt = true
		case "citations.externalIds":
			hasCiteExt = true
		}
		if strings.HasPrefix(f, "references") {
			wantRefs = true
		}
		if strings.HasPrefix(f, "citations") {
			wantCites = true
		}
	}
	out := make([]string, 0, len(fields)+2)
	out = append(out, fields...)
	if wantRefs && !hasRefExt {
		out = append(out, "references.externalIds")
	}
	if wantCites && !hasCiteExt {
		out = append(out, "citations.externalIds")
	}
	return out
}
