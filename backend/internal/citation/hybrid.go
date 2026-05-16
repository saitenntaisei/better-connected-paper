package citation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// perLayerSupplementBudget caps each parallel supplement layer's
// wall-clock time. The OpenCitations layer can legitimately return
// 30k+ citers for popular DOIs (AlphaFold, Attention Is All You
// Need); after the JSON download, the DOI→W-ID resolver paginates
// through OpenAlex and easily blows past a minute. With racing in
// place, anything still running after this budget is something the
// parallel tertiary either already filled or will fill faster than
// the slow secondary, so cancel it and take the best result we have.
const perLayerSupplementBudget = 15 * time.Second

// PaperProvider is the shape HybridClient routes over. *OpenAlexClient and
// *Client (Semantic Scholar) both already satisfy this implicitly.
type PaperProvider interface {
	Search(ctx context.Context, query string, limit int, fields []string) (*SearchResponse, error)
	GetPaper(ctx context.Context, id string, fields []string) (*Paper, error)
	GetPaperBatch(ctx context.Context, ids []string, fields []string) ([]Paper, error)
}

// HybridClient fronts an OpenAlex primary with up to two supplement layers.
// Secondary (typically OpenCitations) is the cheap first try; Tertiary
// (typically S2 wrapped in ResolvingTertiary) catches arxiv preprints that
// neither OpenAlex nor OpenCitations indexes. Supplements run in order and
// stop on the first layer that returns non-empty refs/cites, keeping S2's
// hostile rate limit off the hot path while still reaching papers like Octo
// whose refs only exist in S2's index.
type HybridClient struct {
	Primary   PaperProvider
	Secondary PaperProvider // nilable — absent means primary-only
	Tertiary  PaperProvider // nilable — last-resort supplement after Secondary
	Logger    *slog.Logger
}

// Search goes to the primary. OpenAlex's 10 req/s ceiling is the only reason
// this hybrid exists; S2's anonymous pool is too slow to front a search box.
func (h *HybridClient) Search(ctx context.Context, query string, limit int, fields []string) (*SearchResponse, error) {
	return h.Primary.Search(ctx, query, limit, fields)
}

// GetPaper routes by ID shape, then supplements primary responses that came
// back with an empty reference list when the caller asked for refs and an
// alternative ID (DOI / arxiv) is available.
func (h *HybridClient) GetPaper(ctx context.Context, id string, fields []string) (*Paper, error) {
	if isSecondaryID(id) {
		return h.fetchSecondary(ctx, id, fields)
	}

	p, primaryErr := h.Primary.GetPaper(ctx, id, fields)
	if primaryErr != nil {
		// Primary errored — try to salvage via secondary if we can construct a
		// lookup ID from the original input (DOI/arxiv passthrough).
		if fallbackID := secondaryLookupID(nil, id); fallbackID != "" && h.Secondary != nil {
			return h.fetchSecondary(ctx, fallbackID, fields)
		}
		return nil, primaryErr
	}

	if !h.needsSupplement(p, fields) {
		return p, nil
	}

	// Race the supplement chain (Secondary || Tertiary) in parallel.
	// The first layer to return a narrowing merge cancels its peers so
	// a slow OpenCitations resolver chasing 32k citers on AlphaFold
	// can't keep dragging out the FG seed-fetch after S2 already filled
	// the gap. Sequential walks were costing 6–15 s on Octo and 120+ s
	// on AlphaFold; first-success-wins keeps the wall-clock at the
	// fastest layer's response time.
	current := p
	skipSecondary := isArxivPaper(current)
	chain := h.supplementChain()
	results := make([]*Paper, len(chain))
	layerCtxs := make([]context.Context, len(chain))
	cancels := make([]context.CancelFunc, len(chain))
	// Pre-build every layer's ctx+cancel BEFORE launching any goroutine.
	// cancelLosers walks `cancels` from inside the per-layer goroutine,
	// and a winner racing the next iteration's `cancels[i+1] = cancel`
	// write trips -race. Splitting into write-then-read phases lets the
	// goroutines treat the slice as read-only.
	for i, layer := range chain {
		if skipSecondary && layer.label == "secondary" {
			// OpenCitations doesn't index arxiv preprints — every
			// secondary call on an arxiv DOI returns "non-narrowing"
			// and just costs S2 a parallel token for no gain.
			continue
		}
		layerCtxs[i], cancels[i] = context.WithTimeout(ctx, perLayerSupplementBudget)
	}
	var (
		wg         sync.WaitGroup
		cancelOnce sync.Once
	)
	cancelLosers := func(winner int) {
		cancelOnce.Do(func() {
			for j, c := range cancels {
				if j != winner && c != nil {
					c()
				}
			}
		})
	}
	for i, layer := range chain {
		if cancels[i] == nil {
			continue
		}
		wg.Add(1)
		go func(i int, layer supplementLayer) {
			defer wg.Done()
			results[i] = h.trySupplement(layerCtxs[i], p, id, fields, layer.provider, layer.label)
			if results[i] != nil {
				cancelLosers(i)
			}
		}(i, layer)
	}
	wg.Wait()
	for _, c := range cancels {
		if c != nil {
			c()
		}
	}
	for _, merged := range results {
		if merged == nil {
			continue
		}
		// Each surviving result is already (primary ∪ layer-data) per
		// trySupplement's applyMerge. Union them so a late secondary
		// with a bigger refs list still contributes if it landed
		// before being cancelled.
		current = mergeFromSecondary(current, merged)
	}

	if current == p && h.Logger != nil {
		// In defer-ar5iv mode the chain is intentionally short-circuited
		// after direct lookup, so the "no match" outcome there reflects
		// policy, not a missing index. Use an INFO-level message that
		// names the policy so the warn channel stays for real misses.
		if perPaperSupplementSkipped(ctx) {
			h.Logger.Info("hybrid: supplement deferred (direct lookup only)", "id", id, "title", p.Title)
		} else {
			h.Logger.Warn("hybrid: no supplement match found", "id", id, "title", p.Title)
		}
	}
	return current, nil
}

// isArxivPaper reports whether a paper looks like an arxiv preprint —
// either it carries an ArXiv external id or its DOI lives under the
// 10.48550/arxiv. prefix arxiv mints for new preprints. Used to skip
// supplement providers (OpenCitations) that demonstrably never have
// data for arxiv preprints.
func isArxivPaper(p *Paper) bool {
	if p == nil {
		return false
	}
	if strings.TrimSpace(p.ExternalIDs["ArXiv"]) != "" {
		return true
	}
	doi := strings.ToLower(strings.TrimSpace(p.ExternalIDs["DOI"]))
	return strings.HasPrefix(doi, "10.48550/arxiv.")
}

type supplementLayer struct {
	provider PaperProvider
	label    string
}

func (h *HybridClient) supplementChain() []supplementLayer {
	chain := make([]supplementLayer, 0, 2)
	if h.Secondary != nil {
		chain = append(chain, supplementLayer{provider: h.Secondary, label: "secondary"})
	}
	if h.Tertiary != nil {
		chain = append(chain, supplementLayer{provider: h.Tertiary, label: "tertiary"})
	}
	return chain
}

// trySupplement attempts to fill a primary paper's ref/cite gap from one
// provider. Returns the merged paper on success, or nil when the layer
// didn't actually narrow the gap (empty response, un-improving merge,
// 404 / rate-limit, etc.). Non-retryable direct-lookup errors are logged
// and abort the layer (no title fallback for that provider), but the
// caller still moves on to the next layer — one misbehaving provider
// shouldn't veto later ones.
//
// Rate-limit handling: the underlying client already exhausted its own
// retry budget before surfacing ErrRateLimited, so hammering the same
// provider with sibling/title retries just burns another 2-3 retry cycles
// for the same 429. On first ErrRateLimited we abort this layer; the
// outer chain still advances to the next provider (e.g. tertiary after
// secondary limits).
func (h *HybridClient) trySupplement(ctx context.Context, primary *Paper, id string, fields []string, provider PaperProvider, label string) *Paper {
	if provider == nil {
		return nil
	}
	if lookupID := secondaryLookupID(primary, id); lookupID != "" {
		sp, err := provider.GetPaper(ctx, lookupID, fields)
		if err == nil && sp != nil {
			if merged := h.applyMerge(primary, sp, id, label+":"+lookupID); merged != nil {
				return merged
			}
			// Non-nil response but nothing narrowed — log the shape so a
			// silent "no supplement match found" at the top level is
			// diagnosable (is the provider empty, or did translate drop
			// everything?).
			if h.Logger != nil {
				h.Logger.Info("hybrid: "+label+" direct lookup returned non-narrowing",
					"id", id, "lookup", lookupID,
					"sp_refs", len(sp.References), "sp_cites", len(sp.Citations),
					"primary_refs", len(primary.References), "primary_cites", len(primary.Citations),
				)
			}
		}
		if err != nil {
			if errors.Is(err, ErrRateLimited) {
				if h.Logger != nil {
					h.Logger.Warn("hybrid: "+label+" direct lookup rate limited; skipping sibling/title for this layer", "id", id, "lookup", lookupID)
				}
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				if h.Logger != nil {
					h.Logger.Warn("hybrid: "+label+" direct lookup failed", "id", id, "lookup", lookupID, "err", err)
				}
				return nil
			}
		}
	}
	// Deferred-ar5iv FG: cap the supplement chain at the single direct
	// lookup. The two remaining paths (arxiv-sibling probe and byTitle
	// fallback) each cost 2 sequential S2 calls = ~6 s at the anonymous
	// 1-req/3-s rate, which dominated the perf tail for arxiv preprints
	// whose direct DOI lookup missed (e.g. word2vec, LAPGAN seed fetches
	// at 13 s). The forced-sync BG rerun re-enters this path with the
	// flag off and persists the enriched paper_links so the next FG
	// gets a cache hit before the chain ever runs again.
	if perPaperSupplementSkipped(ctx) {
		return nil
	}
	// Direct lookup missed — for published arxiv preprints the primary DOI is
	// often a conference/proceedings DOI that S2 doesn't index, while S2 DOES
	// index the paper under its arxiv-sibling DOI (e.g. 10.48550/arXiv.XXXX).
	// Probe OpenAlex for a same-title same-year work whose DOI matches the
	// arxiv form and retry the direct lookup against THAT DOI before falling
	// back to the noisier title search. Skipped when the primary DOI already
	// IS an arxiv DOI (nothing new to try).
	if altID := h.arxivSiblingLookupID(ctx, primary); altID != "" {
		sp, err := provider.GetPaper(ctx, altID, fields)
		if err == nil && sp != nil {
			if merged := h.applyMerge(primary, sp, id, label+":sibling:"+altID); merged != nil {
				return merged
			}
			if h.Logger != nil {
				h.Logger.Info("hybrid: "+label+" sibling lookup returned non-narrowing",
					"id", id, "lookup", altID,
					"sp_refs", len(sp.References), "sp_cites", len(sp.Citations),
					"primary_refs", len(primary.References), "primary_cites", len(primary.Citations),
				)
			}
		}
		if err != nil {
			if errors.Is(err, ErrRateLimited) {
				if h.Logger != nil {
					h.Logger.Warn("hybrid: "+label+" sibling lookup rate limited; skipping title for this layer", "id", id, "lookup", altID)
				}
				return nil
			}
			if !errors.Is(err, ErrNotFound) && h.Logger != nil {
				h.Logger.Warn("hybrid: "+label+" sibling lookup failed", "id", id, "lookup", altID, "err", err)
			}
		}
	}
	if sp := h.byTitle(ctx, primary, fields, provider); sp != nil {
		if merged := h.applyMerge(primary, sp, id, label+":title"); merged != nil {
			return merged
		}
	}
	return nil
}

// arxivSiblingLookupID returns a "DOI:10.48550/arXiv.XXXX" lookup id when
// the primary paper has a published-version DOI but a same-title arxiv-DOI
// sibling also exists in OpenAlex. Returns "" when the primary's own DOI is
// already arxiv-form, when no sibling is found, or when the probe fails.
func (h *HybridClient) arxivSiblingLookupID(ctx context.Context, primary *Paper) string {
	if primary == nil || primary.Title == "" || h.Primary == nil {
		return ""
	}
	primaryDOI := strings.ToLower(primary.ExternalIDs["DOI"])
	if strings.Contains(primaryDOI, "10.48550/arxiv") {
		return ""
	}
	resp, err := h.Primary.Search(ctx, primary.Title, 5, nil)
	if err != nil || resp == nil {
		return ""
	}
	wantTitle := normalizeTitle(primary.Title)
	for i := range resp.Data {
		c := &resp.Data[i]
		if normalizeTitle(c.Title) != wantTitle {
			continue
		}
		if primary.Year > 0 && c.Year > 0 && absInt(primary.Year-c.Year) > 1 {
			continue
		}
		doi := strings.ToLower(strings.TrimSpace(c.ExternalIDs["DOI"]))
		if doi == "" || !strings.Contains(doi, "10.48550/arxiv") {
			continue
		}
		return "DOI:" + doi
	}
	return ""
}

// applyMerge runs the merge and only returns the result when it actually
// narrows a gap (more refs, more cites, or resolves CitationsUnknown).
// Nil return signals "no progress; try the next layer."
func (h *HybridClient) applyMerge(primary, supplement *Paper, originalID, via string) *Paper {
	merged := mergeFromSecondary(primary, supplement)
	if !supplementNarrowedGap(primary, merged) {
		return nil
	}
	if h.Logger != nil {
		h.Logger.Info("hybrid: supplemented refs/cites",
			"id", primary.PaperID,
			"original_id", originalID,
			"via", via,
			"primary_refs", len(primary.References),
			"merged_refs", len(merged.References),
			"primary_cites", len(primary.Citations),
			"merged_cites", len(merged.Citations),
		)
	}
	return merged
}

// supplementNarrowedGap returns true when `after` carries strictly more
// refs, strictly more cites, or resolves a previously-unknown cite set.
// Anything else means the layer didn't improve on what we already had.
func supplementNarrowedGap(before, after *Paper) bool {
	if len(after.References) > len(before.References) {
		return true
	}
	if len(after.Citations) > len(before.Citations) {
		return true
	}
	if before.CitationsUnknown && !after.CitationsUnknown {
		return true
	}
	return false
}

// byTitle searches `provider` for the primary's title and requires an exact
// normalized title match plus ±1 year tolerance before accepting the hit.
// Strict matching keeps us from merging refs off an unrelated similarly
// titled paper.
func (h *HybridClient) byTitle(ctx context.Context, primary *Paper, fields []string, provider PaperProvider) *Paper {
	if primary == nil || primary.Title == "" || provider == nil {
		return nil
	}
	resp, err := provider.Search(ctx, primary.Title, 5, []string{"paperId", "title", "year", "externalIds"})
	if err != nil || resp == nil || len(resp.Data) == 0 {
		return nil
	}

	wantTitle := normalizeTitle(primary.Title)
	var hit *Paper
	for i := range resp.Data {
		c := &resp.Data[i]
		if c.PaperID == "" || normalizeTitle(c.Title) != wantTitle {
			continue
		}
		if primary.Year > 0 && c.Year > 0 && absInt(primary.Year-c.Year) > 1 {
			continue
		}
		hit = c
		break
	}
	if hit == nil {
		return nil
	}

	full, err := provider.GetPaper(ctx, hit.PaperID, fields)
	if err != nil || full == nil {
		return nil
	}
	return full
}

// GetPaperBatch splits the id list by provider shape, queries each in
// parallel (sequential for simplicity; each client has its own limiter), and
// unions the results. When refs are requested and the primary returned
// arxiv preprints with empty refs but a DOI, supplementBatchRefs fires ONE
// tertiary batch call (with DOI resolution amortized across every paper)
// to fill them — this is what surfaces the recent-robotics-preprint cluster
// (OpenVLA, π₀, DROID, CrossFormer, RDT-1B) in the first-hop graph scoring.
func (h *HybridClient) GetPaperBatch(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	var primaryIDs, secondaryIDs []string
	for _, id := range ids {
		if isSecondaryID(id) {
			secondaryIDs = append(secondaryIDs, id)
		} else {
			primaryIDs = append(primaryIDs, id)
		}
	}

	out := make([]Paper, 0, len(ids))

	if len(primaryIDs) > 0 {
		ps, err := h.Primary.GetPaperBatch(ctx, primaryIDs, fields)
		if err != nil {
			return nil, fmt.Errorf("hybrid primary batch: %w", err)
		}
		out = append(out, ps...)
	}

	if len(secondaryIDs) > 0 {
		if h.Secondary == nil {
			return nil, fmt.Errorf("hybrid: %d secondary-space IDs received but secondary provider is not configured", len(secondaryIDs))
		}
		ss, err := h.Secondary.GetPaperBatch(ctx, secondaryIDs, fields)
		if err != nil {
			return nil, fmt.Errorf("hybrid secondary batch: %w", err)
		}
		out = append(out, ss...)
	}

	h.supplementBatchRefs(ctx, out, fields)

	return out, nil
}

// supplementBatchRefs patches arxiv preprints whose primary returned empty
// refs by routing them through Tertiary in ONE batch call keyed by DOI.
// Connected-Papers-style scoring on the Octo seed depends on this: biblio
// coupling collapses to 0 for every first-hop candidate with empty refs,
// dropping the modern preprint cluster (OpenVLA/π₀/etc.) out of top-40.
// The call is best-effort — a tertiary failure degrades the graph but
// doesn't break the request.
func (h *HybridClient) supplementBatchRefs(ctx context.Context, papers []Paper, fields []string) {
	if h.Tertiary == nil || !requestsReferences(fields) || len(papers) == 0 {
		return
	}
	type pending struct {
		idx int
		doi string
	}
	queue := make([]pending, 0, len(papers))
	seenDOI := make(map[string]struct{}, len(papers))
	for i := range papers {
		if len(papers[i].References) > 0 {
			continue
		}
		doi := strings.ToLower(strings.TrimSpace(papers[i].ExternalIDs["DOI"]))
		if doi == "" {
			continue
		}
		if _, dup := seenDOI[doi]; dup {
			continue
		}
		seenDOI[doi] = struct{}{}
		queue = append(queue, pending{idx: i, doi: doi})
	}
	if len(queue) == 0 {
		return
	}
	doiIDs := make([]string, len(queue))
	for i, q := range queue {
		doiIDs[i] = "DOI:" + q.doi
	}
	results, err := h.Tertiary.GetPaperBatch(ctx, doiIDs, []string{"paperId", "externalIds", "references.externalIds"})
	if err != nil {
		if h.Logger != nil {
			h.Logger.Warn("hybrid: batch arxiv supplement failed", "count", len(queue), "err", err)
		}
		return
	}
	if len(results) == 0 {
		return
	}
	byDOI := make(map[string][]Paper, len(results))
	for i := range results {
		d := strings.ToLower(strings.TrimSpace(results[i].ExternalIDs["DOI"]))
		if d == "" || len(results[i].References) == 0 {
			continue
		}
		byDOI[d] = results[i].References
	}
	filled := 0
	for _, q := range queue {
		refs, ok := byDOI[q.doi]
		if !ok {
			continue
		}
		papers[q.idx].References = refs
		if papers[q.idx].ReferenceCount < len(refs) {
			papers[q.idx].ReferenceCount = len(refs)
		}
		filled++
	}
	if h.Logger != nil && filled > 0 {
		h.Logger.Info("hybrid: batch arxiv supplement", "queued", len(queue), "filled", filled)
	}
}

func (h *HybridClient) fetchSecondary(ctx context.Context, id string, fields []string) (*Paper, error) {
	if h.Secondary == nil {
		return nil, ErrNotFound
	}
	return h.Secondary.GetPaper(ctx, id, fields)
}

// needsSupplement returns true when the caller asked for refs/cites but the
// primary came back empty for a paper we have an alternative ID for. The
// ReferenceCount>0 heuristic avoids hitting the secondary for papers that
// genuinely have no refs (rare but real).
func (h *HybridClient) needsSupplement(p *Paper, fields []string) bool {
	if p == nil {
		return false
	}
	wantsRefs := requestsReferences(fields)
	wantsCites := requestsCitations(fields)
	if !wantsRefs && !wantsCites {
		return false
	}
	refsGap := wantsRefs && len(p.References) == 0 && (p.ReferenceCount > 0 || hasExternalDOIOrArxiv(p))
	citesGap := wantsCites && p.CitationsUnknown
	return refsGap || citesGap
}

// mergeFromSecondary preserves the primary paper's identity (PaperID stays
// the primary's W-ID, so cache keys and request paths are stable) but swaps
// in the secondary's refs/cites where the primary came back empty. We only
// replace a non-empty primary list when secondary is strictly larger — that
// matters with providers like OpenCitations whose coverage on a given paper
// can be narrower than OpenAlex's, and we don't want to shrink a complete
// primary list into a partial secondary one.
func mergeFromSecondary(primary, secondary *Paper) *Paper {
	merged := *primary
	if len(secondary.References) > len(primary.References) {
		merged.References = secondary.References
		if merged.ReferenceCount < len(secondary.References) {
			merged.ReferenceCount = len(secondary.References)
		}
	}
	switch {
	case len(secondary.Citations) > len(primary.Citations):
		merged.Citations = secondary.Citations
		merged.CitationsUnknown = false
	case primary.CitationsUnknown && secondary.CitationsUnknown:
		merged.CitationsUnknown = true
	case primary.CitationsUnknown && len(secondary.Citations) > 0:
		merged.Citations = secondary.Citations
		merged.CitationsUnknown = false
	}
	if secondary.PaperID != "" && secondary.PaperID != primary.PaperID {
		merged.MergedFromID = secondary.PaperID
	}
	return &merged
}

// isSecondaryID recognises a 40-char hex string (Semantic Scholar paperId).
// OpenAlex W-IDs, DOIs, and arxiv IDs never match.
func isSecondaryID(id string) bool {
	id = strings.TrimSpace(id)
	if len(id) != 40 {
		return false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// secondaryLookupID converts a paper / original query ID into the form S2's
// GetPaper accepts. Returns "" if no usable identifier exists.
func secondaryLookupID(p *Paper, originalID string) string {
	if p != nil {
		if doi, ok := p.ExternalIDs["DOI"]; ok && doi != "" {
			return "DOI:" + doi
		}
		if arx, ok := p.ExternalIDs["ArXiv"]; ok && arx != "" {
			return "arXiv:" + arx
		}
	}
	trimmed := strings.TrimSpace(originalID)
	switch {
	case strings.HasPrefix(trimmed, "10."):
		return "DOI:" + trimmed
	case strings.HasPrefix(strings.ToLower(trimmed), "doi:"):
		return "DOI:" + trimmed[4:]
	case strings.HasPrefix(strings.ToLower(trimmed), "arxiv:"):
		return "arXiv:" + trimmed[len("arxiv:"):]
	case strings.HasPrefix(trimmed, "https://doi.org/"):
		return "DOI:" + strings.TrimPrefix(trimmed, "https://doi.org/")
	}
	return ""
}

func hasExternalDOIOrArxiv(p *Paper) bool {
	if p == nil {
		return false
	}
	if doi, ok := p.ExternalIDs["DOI"]; ok && doi != "" {
		return true
	}
	if arx, ok := p.ExternalIDs["ArXiv"]; ok && arx != "" {
		return true
	}
	return false
}

// requestsReferences mirrors requestsCitations for the refs-side trigger.
func requestsReferences(fields []string) bool {
	for _, f := range fields {
		if strings.HasPrefix(f, "references") {
			return true
		}
	}
	return false
}

// normalizeTitle lowercases, drops non-alphanumeric runes, and collapses
// whitespace so two titles equal each other iff they're a strict text match
// ignoring punctuation/case. Used for the secondary title-search fallback,
// where a near-match ("Octo:" vs "Octo") should still be a hit.
func normalizeTitle(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			prevSpace = false
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevSpace = false
		default:
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
