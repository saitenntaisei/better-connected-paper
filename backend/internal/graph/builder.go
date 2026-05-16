package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saitenntaisei/better-connected-paper/internal/citation"
)

// S2 is the subset of citation.Client behavior used by the builder.
// Extracted as an interface so tests can stub it without network I/O.
type S2 interface {
	GetPaper(ctx context.Context, id string, fields []string) (*citation.Paper, error)
	GetPaperBatch(ctx context.Context, ids []string, fields []string) ([]citation.Paper, error)
}

// Cache is the subset of store.DB behavior the Builder uses to avoid
// round-tripping to Semantic Scholar for papers it has already fetched.
// All methods must tolerate a nil receiver so tests and no-DB deployments
// can pass a nil Cache and get a straight S2-only path.
type Cache interface {
	// GetPapersWithLinks returns cached papers whose refs/cites lists have
	// been persisted. Missing papers and papers without links come back as
	// "not in result" so the caller falls back to S2.
	GetPapersWithLinks(ctx context.Context, ids []string) ([]citation.Paper, error)
	UpsertPapers(ctx context.Context, papers []citation.Paper) error
	ReplacePaperLinks(ctx context.Context, paperID string, refs, cites []string) error
	// InvalidateGraph drops the cached JSON payload for a seed.
	InvalidateGraph(ctx context.Context, seedID string) error
	// StoreGraph replaces the cached JSON payload for a seed. Used by the
	// deferred-ar5iv background goroutine: after rerunning Build with
	// full supplements, the enriched graph is written here directly so
	// the next /api/graph/build request returns it from the cache
	// instead of rebuilding (turns refresh into an instant cache hit
	// rather than the ~10 s rebuild it would otherwise take).
	StoreGraph(ctx context.Context, seedID string, payload []byte) error
}

// Builder constructs the directed graph around a seed paper.
type Builder struct {
	S2 S2
	// Recommender, when wired (typically the *citation.ResolvingTertiary
	// fronting S2), is consulted only for sparse seeds — papers that come
	// back from S2/OpenAlex with too few refs+cites for the citation-based
	// 2-hop expansion to surface a meaningful neighborhood. Recent arxiv
	// preprints (the AsyncVLA case) routinely hit this path. Recs land in
	// the first-hop candidate pool so the existing ranking + edge logic
	// handles them like any other neighbor.
	Recommender citation.Recommender
	// Logger receives diagnostic events from the build (sparse-seed
	// supplements, recommender hits/misses). Optional; nil silences output.
	Logger    *slog.Logger
	Cache     Cache
	MaxNodes  int           // default 40
	BatchSize int           // S2 batch cap: 500, we use 100 to keep responses small
	Timeout   time.Duration // default 90s
	Now       func() time.Time

	// TwoHopSupport is the minimum number of first-hop papers a second-hop
	// candidate must connect to before we consider it. 2 means "shared by at
	// least two first-hop neighbors", which is the standard bibliographic-
	// coupling signal for bridge papers. Default 2.
	TwoHopSupport int

	// MaxBridgeCandidates caps how many 2-hop papers we hydrate for scoring.
	// A high-degree seed can produce thousands of 2-hop bridges, so we rank
	// by support count and take the top N. Default 200.
	MaxBridgeCandidates int

	// SimilarityEdgeThreshold is the minimum pairwise similarity weight
	// required to emit a similarity edge in the returned graph. Default 0.15.
	SimilarityEdgeThreshold float64

	// MaxFirstHop caps how many first-hop candidates we fetch. A seminal
	// seed supplemented from Semantic Scholar can surface 1000+ citers, and
	// the anonymous S2 rate limit (≈1 req / 3s) turns that into a multi-
	// minute fetch that routinely rate-limits out. Ranking for a MaxNodes
	// graph stays meaningful with a few hundred samples. Default 300.
	MaxFirstHop int

	// RecommendSparseThreshold is the refs+cites count below which the
	// builder reaches for Recommender to broaden the candidate pool. Set
	// negative to disable; zero picks the default (10), low enough that
	// well-cited seeds skip the extra round-trip but high enough to catch
	// recent preprints whose direct refs have not yet been ingested.
	RecommendSparseThreshold int

	// RecommendLimit caps how many recommendations are pulled per sparse
	// seed. Default 30 — enough to roughly match a Connected-Papers cluster
	// for new papers without dominating the firstHop budget.
	RecommendLimit int

	// Embedder, when wired (typically the same *citation.ResolvingTertiary
	// fronting S2), produces specter_v2 paper embeddings that the builder
	// uses to add a second similarity-edge layer. For seeds whose neighbors
	// are all freshly-posted preprints — the AsyncVLA recs cluster — biblio
	// coupling collapses to 0 (no provider has indexed the refs yet), so
	// without embedding edges the cluster renders as disconnected dots.
	Embedder citation.Embedder

	// EmbeddingSimilarityThreshold is the minimum cosine on specter_v2 for
	// considering an embedding-similarity edge. Default 0.7 — a soft floor;
	// the per-node top-K trim is what actually controls visual density.
	EmbeddingSimilarityThreshold float64

	// EmbeddingTopK keeps each node's strongest K embedding-similarity
	// neighbours (rather than every pair above threshold). Default 5 —
	// roughly Connected-Papers' density. Raising it grows the graph
	// quadratically so prefer adjusting threshold for noisier domains.
	EmbeddingTopK int

	// RefsBackfillBudget caps total paginated /references fallback calls
	// per build (across firstHop / bridges / full fetch chunks). Default
	// 20 — at 1 RPS keyed that's ≈20 s, comfortably below the Vercel 60 s
	// function cap once /paper, /recs, and /embedding calls are added in.
	// Set negative to disable the cap (unbounded).
	RefsBackfillBudget int

	// DeferAr5iv runs the initial Build with ar5iv suppressed (the user
	// gets the recs + embedding-similarity layer immediately, in ≈7 s
	// instead of ≈15 s), then spawns a background goroutine that reruns
	// the build with ar5iv enabled so the next /api/graph/build request
	// for the same seed serves the enriched cite-arrow graph from the
	// paper_links cache. Set false to keep ar5iv inline (correct, just
	// slower) — Vercel-style serverless deployments where a goroutine
	// can't outlive the response should leave this off.
	DeferAr5iv bool

	// deferInFlight tracks which seedIDs already have a deferred-ar5iv
	// goroutine running, so back-to-back FG requests for the same seed
	// (e.g. user retry, browser pre-flight) don't each kick off a 3-min
	// rebuild hitting S2/OpenAlex/ar5iv twice for the same data and
	// racing on StoreGraph. Sync.Map keyed on seedID with struct{}{}
	// value; spawnDeferredAr5iv claims via LoadOrStore and the goroutine
	// clears on exit.
	deferInFlight sync.Map
}

// seedFields are requested for the initial /paper/{id} call: full metadata
// plus nested reference/citation paperIds for similarity computation.
var seedFields = []string{
	"paperId", "title", "abstract", "year", "venue", "authors",
	"citationCount", "referenceCount", "influentialCitationCount",
	"externalIds", "url",
	"references.paperId",
	"citations.paperId",
}

// minimalLinkFields is the cheap fetch used to explore the 1-hop cloud:
// paper id, citationCount (the Salton denominator scale for the coCite
// term — without it the coCite score collapses to 0), and the refs id
// list. Cites are NOT requested here: rank, twoHopSupport, and the
// final cite-edge construction all read from firstHop's *refs* (not
// cites), and asking OpenAlex for citations.paperId triggers a per-paper
// /works?filter=cites: enrichment call that costs ~3 s across a 30-paper
// first hop for no scoring or edge value.
//
// `title` and `year` are requested even though the OpenAlex client
// always returns them: rankCandidates reads p.Title for seed-alias
// dedupe and p.Year for the year-proximity bonus, so we ask explicitly
// to keep behaviour stable across providers — Semantic Scholar honours
// the field list and silently drops fields the caller didn't request,
// which would zero out both signals on a hypothetical S2-primary
// configuration.
var minimalLinkFields = []string{
	"paperId",
	"title",
	"year",
	"citationCount",
	"references.paperId",
}

// firstHopFieldsLean is the deferred-mode-only variant of
// minimalLinkFields that omits even the refs id list. With bridges
// skipped on the FG path the refs would only be consumed by biblio
// coupling — and the embedding-similarity layer already covers that
// signal, so the supplementBatchRefs round-trip (S2 inline batch +
// DOI resolver) is pure overhead on the hot path. The forced-sync
// background rerun goes back to minimalLinkFields so the cache row
// landed by StoreGraph is the full enriched build.
// Title/year stay in the list for the same dedupe-and-year-bonus
// reason as minimalLinkFields above.
var firstHopFieldsLean = []string{
	"paperId",
	"title",
	"year",
	"citationCount",
}

// bridgeLinkFields is the metadata fetch used for 2-hop bridge candidates.
// Refs are deliberately omitted: bridges are ranked by 2-hop support (a
// count derived from firstHop's refs, which we already have), and biblio
// coupling between bridges and the rest of the graph stays at 0 — the
// embedding-similarity layer already wires bridges in. Skipping refs
// here skips the ar5iv + paginated /references supplement chain for the
// entire bridges batch, the biggest single perf win on sparse-seed builds.
// Cites enrichment is also deliberately skipped: most famous bridges
// have >100 citers, the OpenAlex client would then flag them
// CitationsUnknown, and we'd have paid a per-paper fanout of cites
// requests for nothing. Title/year stay in the list for rankCandidates'
// dedupe + year bonus.
var bridgeLinkFields = []string{
	"paperId",
	"title",
	"year",
	"citationCount",
}

// fullNodeFields hydrates a selected node for the final response (title,
// authors, abstract, etc.). Refs/cites are NOT requested here — by this
// point firstHopByID/bridgesByID already carry the supplemented refs
// (Build merges them into the full slice after the fetch), so re-firing
// the ar5iv + paginated chain for full-fetch would duplicate the work
// the firstHop phase already paid for. We only pay this for at most
// MaxNodes papers.
var fullNodeFields = []string{
	"paperId", "title", "abstract", "year", "venue", "authors",
	"citationCount", "referenceCount", "externalIds", "url",
}

// Build expands the graph around seedID. The expansion is staged (cheap
// minimal-fields fetch for the first hop, full-metadata fetch only for the
// pruned top-MaxNodes subset) so a high-degree seed can't blow the request
// budget.
func (b *Builder) Build(ctx context.Context, seedID string) (*Response, error) {
	b.applyDefaults()
	if b.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.Timeout)
		defer cancel()
	}
	// Cap the per-build paginated /references fallback so a wide first-hop
	// chunked across multiple GetPaperBatch calls can't stack 60 s of
	// 1-RPS calls and blow the Vercel function cap.
	ctx = citation.WithRefsBackfillBudget(ctx, b.refsBackfillBudget())

	// Deferred-ar5iv mode: drop ar5iv from the sync path, then spawn a
	// background re-build (with ar5iv forced on) and invalidate the
	// cached graphs payload so the next request rebuilds against the
	// freshly populated paper_links rows. The background goroutine
	// itself bypasses defer via IsForceSyncAr5iv to avoid recursion.
	deferAr5iv := b.DeferAr5iv && !citation.IsForceSyncAr5iv(ctx)
	if deferAr5iv {
		ctx = citation.WithSkipAr5iv(ctx, true)
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}

	seed, err := b.fetchSeed(ctx, seedID)
	if err != nil {
		return nil, fmt.Errorf("fetch seed: %w", err)
	}
	if seed.PaperID == "" {
		return nil, fmt.Errorf("seed %q returned no paperId", seedID)
	}

	seedRefs := seed.RefIDs()
	seedCitedBy := seed.CitedByIDs()

	// Seed may carry a secondary-provider alias when hybrid supplemented it
	// (e.g. S2 hex when primary is OpenAlex). First-hop papers fetched from
	// the secondary reference this alias; without canonicalization they
	// materialize as a duplicate seed node with all the cite edges pointing
	// there instead of the requested seed.
	seedAlias := seed.MergedFromID
	excluded := []string{seed.PaperID}
	if seedAlias != "" {
		excluded = append(excluded, seedAlias)
	}
	recCandidates := b.recommendForSparseSeed(ctx, seed)
	candidates := append([]string(nil), seedRefs...)
	candidates = append(candidates, seedCitedBy...)
	candidates = append(candidates, recCandidates...)
	firstHopIDs := dedupeExcluding(candidates, excluded...)
	if b.MaxFirstHop > 0 && len(firstHopIDs) > b.MaxFirstHop {
		firstHopIDs = firstHopIDs[:b.MaxFirstHop]
	}
	// In deferred-ar5iv FG we never need firstHop's refs: bridges (the
	// only consumer of two-hop support) are skipped above, and biblio
	// coupling for similarity edges is replaced by the specter embedding
	// layer. Asking for "references.paperId" would still fire the entire
	// supplementBatchRefs path inside HybridClient (S2 inline batch +
	// translateRefsBatch + DOI resolver) for nothing — skip it on the
	// hot path and let the forced-sync background rerun pick up the
	// canonical refs into paper_links.
	firstHopFields := minimalLinkFields
	if deferAr5iv {
		firstHopFields = firstHopFieldsLean
	}
	firstHop, err := b.fetchWithCache(ctx, firstHopIDs, firstHopFields)
	if err != nil {
		return nil, fmt.Errorf("fetch first hop: %w", err)
	}
	canonicalizeSeedAlias(firstHop, seedAlias, seed.PaperID)
	firstHopByID := indexPapers(firstHop)

	twoHopSupport := countTwoHopSupport(firstHop, seed.PaperID, firstHopByID)

	// Bridge candidates: 2-hop papers with enough first-hop support to be
	// worth hydrating. Capped to avoid a blow-out for high-degree seeds.
	// In deferred-ar5iv mode the initial response skips bridges entirely
	// — recs + seed-cites already provide ~30 candidates, the embedding
	// layer wires similarity edges between them, and an extra
	// MaxBridgeCandidates batch fetch on the hot path adds ~3 s for
	// nodes that mostly fall outside the top-MaxNodes cut anyway. The
	// forced-sync background rerun (citation.IsForceSyncAr5iv) restores
	// bridges so the enriched cached graph includes them on refresh.
	var bridges []citation.Paper
	if !deferAr5iv {
		bridgeIDs := selectBridgeIDs(twoHopSupport, b.TwoHopSupport, b.MaxBridgeCandidates)
		if seedAlias != "" {
			bridgeIDs = filterOut(bridgeIDs, seedAlias)
		}
		var bErr error
		bridges, bErr = b.fetchWithCache(ctx, bridgeIDs, bridgeLinkFields)
		if bErr != nil {
			return nil, fmt.Errorf("fetch two-hop bridges: %w", bErr)
		}
		canonicalizeSeedAlias(bridges, seedAlias, seed.PaperID)
	}
	bridgesByID := indexPapers(bridges)

	scored := rankCandidates(seed, firstHop, bridges)
	if len(scored) > b.MaxNodes-1 {
		scored = scored[:b.MaxNodes-1]
	}

	selectedIDs := make([]string, 0, len(scored)+1)
	selectedIDs = append(selectedIDs, seed.PaperID)
	for _, s := range scored {
		selectedIDs = append(selectedIDs, s.id)
	}

	// embedder only reads externalIds, which OpenAlex always populates
	// on every fetch (the client's hard-coded `select` includes `ids`),
	// so the seed + firstHopByID / bridgesByID maps already carry
	// everything the SPECTER batch needs. Kick the S2 round-trip off
	// here so its 3-5 s rate-limited wall time overlaps the parallel
	// OpenAlex full-fetch instead of stacking sequentially after it.
	// linkSource (the post-full-fetch source-of-truth) is what the
	// in-memory citation/biblio edge passes still read; only the
	// embedder gets this preliminary view.
	embedderLinks := make(map[string]citation.Paper, len(selectedIDs))
	embedderLinks[seed.PaperID] = *seed
	for _, id := range selectedIDs[1:] {
		if p, ok := firstHopByID[id]; ok {
			embedderLinks[id] = p
		} else if p, ok := bridgesByID[id]; ok {
			embedderLinks[id] = p
		}
	}
	var (
		embedderWG    sync.WaitGroup
		embedderEdges []Edge
	)
	embedderWG.Add(1)
	go func() {
		defer embedderWG.Done()
		embedderEdges = b.embeddingSimilarityEdges(ctx, selectedIDs, embedderLinks)
	}()

	full, err := b.fetchWithCache(ctx, selectedIDs[1:], fullNodeFields)
	if err != nil {
		embedderWG.Wait()
		return nil, fmt.Errorf("fetch selected metadata: %w", err)
	}
	canonicalizeSeedAlias(full, seedAlias, seed.PaperID)
	// fullNodeFields omits refs/cites to skip the ar5iv + paginated
	// supplement chain on the final fetch — firstHop and bridges already
	// paid for that work, so copy their populated links into the full
	// slice before persistence and edge construction so linkSource can
	// pick them up via fullByID without re-fetching.
	for i := range full {
		p := &full[i]
		if len(p.References) == 0 {
			if earlier, ok := firstHopByID[p.PaperID]; ok && len(earlier.References) > 0 {
				p.References = earlier.References
			} else if earlier, ok := bridgesByID[p.PaperID]; ok && len(earlier.References) > 0 {
				p.References = earlier.References
			}
		}
		if len(p.Citations) == 0 && !p.CitationsUnknown {
			if earlier, ok := firstHopByID[p.PaperID]; ok && len(earlier.Citations) > 0 {
				p.Citations = earlier.Citations
				p.CitationsUnknown = earlier.CitationsUnknown
			}
		}
	}
	fullByID := indexPapers(full)
	// Skip persistence in deferred-ar5iv FG: this run's papers carry
	// sparse refs (per-paper supplements were intentionally bypassed),
	// and persisting them would let the background rerun's
	// fetchWithCache treat the half-populated rows as cache hits and
	// shadow the ar5iv work the BG specifically came back to do. The
	// forced-sync BG path re-persists with full supplements, so the
	// cache ends up with the enriched state on refresh.
	if !deferAr5iv {
		b.persistFetched(ctx, seed, full)
	}

	nodes := make([]Node, 0, len(scored)+1)
	seedNode := ToNode(*seed)
	seedNode.IsSeed = true
	seedNode.Similarity = 0
	nodes = append(nodes, seedNode)

	for _, s := range scored {
		p, ok := fullByID[s.id]
		if !ok {
			// Second-hop bridges only have minimal fields; fall back so the
			// node still appears even without full metadata.
			if mp, okm := firstHopByID[s.id]; okm {
				p = mp
			} else if mp, okm := bridgesByID[s.id]; okm {
				p = mp
			} else {
				p = citation.Paper{PaperID: s.id}
			}
		}
		n := ToNode(p)
		// s.score = ScoreCP(biblio, coCite) + rankingBonus, which can
		// climb to 1.20 once a high-overlap candidate (ScoreCP ≈ 1.0)
		// also collects the full 0.20 year + cc bonus. Node.Similarity
		// is documented as [0, 1] in types.go and frontend consumers
		// can normalise on that range, so clamp here while preserving
		// the unbounded value inside scoredCandidate for ranking
		// stability.
		sim := s.score
		if sim > 1.0 {
			sim = 1.0
		} else if sim < 0 {
			sim = 0
		}
		n.Similarity = sim
		nodes = append(nodes, n)
	}

	// Edge construction needs ref/cite lists for every selected id; the full
	// fetch drops them for 2-hop bridges, so union with the minimal records.
	linkSource := make(map[string]citation.Paper, len(selectedIDs))
	linkSource[seed.PaperID] = *seed
	for _, id := range selectedIDs {
		if p, ok := fullByID[id]; ok {
			linkSource[id] = p
		} else if p, ok := firstHopByID[id]; ok {
			linkSource[id] = p
		} else if p, ok := bridgesByID[id]; ok {
			linkSource[id] = p
		}
	}

	selectedSet := sliceSet(selectedIDs)
	edges := buildCiteEdges(selectedIDs, selectedSet, linkSource)
	edges = append(edges, buildSimilarityEdges(selectedIDs, linkSource, b.SimilarityEdgeThreshold)...)
	// Join the embedder goroutine kicked off before fetchWithCache. Its
	// edges read externalIds (stable across firstHop/full fetches), so
	// the parallel result is identical to what a serial call after the
	// full fetch would have produced.
	embedderWG.Wait()
	edges = append(edges, embedderEdges...)
	edges = dedupeEdges(edges)

	nodes, edges = pruneOrphanNodes(seedNode.ID, nodes, edges)

	if deferAr5iv {
		b.spawnDeferredAr5iv(seedID)
	}

	return &Response{
		Seed:        seedNode,
		Nodes:       nodes,
		Edges:       edges,
		BuiltAt:     now().UTC(),
		Preliminary: deferAr5iv,
	}, nil
}

// spawnDeferredAr5iv re-runs Build with ar5iv forced on, in a goroutine
// rooted at a fresh context so it outlives the original request. The
// enriched Response is then written back to the graphs cache so the
// next /api/graph/build request returns it from the cache as an
// instant hit — refresh used to pay a ~10 s rebuild even after the
// background populated paper_links, because the rebuild still had to
// walk every fetch / rank / edge phase. Writing the enriched payload
// here turns that refresh into a pure cache hit.
//
// Concurrent FG calls for the same seed are deduplicated via
// deferInFlight: only the first claim spawns; later calls log and
// return so we don't pay the rebuild twice (and don't race on the
// StoreGraph write).
func (b *Builder) spawnDeferredAr5iv(seedID string) {
	if _, loaded := b.deferInFlight.LoadOrStore(seedID, struct{}{}); loaded {
		if b.Logger != nil {
			b.Logger.Info("deferred ar5iv backfill: already in flight; skipping", "seed", seedID)
		}
		return
	}
	go func() {
		defer b.deferInFlight.Delete(seedID)
		bgCtx, cancel := context.WithTimeout(citation.WithForceSyncAr5iv(context.Background()), 3*time.Minute)
		defer cancel()
		if b.Logger != nil {
			b.Logger.Info("deferred ar5iv backfill: starting", "seed", seedID)
		}
		resp, err := b.Build(bgCtx, seedID)
		if err != nil {
			if b.Logger != nil {
				b.Logger.Warn("deferred ar5iv backfill: build failed", "seed", seedID, "err", err)
			}
			return
		}
		if b.Cache != nil && resp != nil {
			payload, mErr := json.Marshal(resp)
			if mErr != nil {
				if b.Logger != nil {
					b.Logger.Warn("deferred ar5iv backfill: marshal failed", "seed", seedID, "err", mErr)
				}
				return
			}
			if sErr := b.Cache.StoreGraph(bgCtx, seedID, payload); sErr != nil && b.Logger != nil {
				b.Logger.Warn("deferred ar5iv backfill: store graph failed", "seed", seedID, "err", sErr)
			}
		}
		if b.Logger != nil {
			b.Logger.Info("deferred ar5iv backfill: completed", "seed", seedID)
		}
	}()
}

// pruneOrphanNodes drops non-seed nodes that participate in zero edges. Such
// nodes score high enough on seed-only signals to make top-N but share no
// structural overlap with any other selected node, so Cytoscape layouts stack
// them at the origin and the visualization reads worse than a smaller but
// fully-connected graph. The seed is preserved unconditionally.
//
// Recommendation candidates rely on the embedding-similarity layer to surface
// edges into the rest of the cluster — if S2 returns no embedding for a rec
// it has no signal to draw, so prune-as-orphan is the right outcome.
func pruneOrphanNodes(seedID string, nodes []Node, edges []Edge) ([]Node, []Edge) {
	if len(nodes) == 0 {
		return nodes, edges
	}
	endpoints := make(map[string]struct{}, len(edges)*2)
	for _, e := range edges {
		endpoints[e.Source] = struct{}{}
		endpoints[e.Target] = struct{}{}
	}
	kept := nodes[:0]
	for _, n := range nodes {
		if n.ID == seedID {
			kept = append(kept, n)
			continue
		}
		if _, ok := endpoints[n.ID]; ok {
			kept = append(kept, n)
		}
	}
	return kept, edges
}

func (b *Builder) applyDefaults() {
	if b.MaxNodes <= 0 {
		b.MaxNodes = 40
	}
	if b.BatchSize <= 0 {
		b.BatchSize = 100
	}
	if b.TwoHopSupport <= 0 {
		b.TwoHopSupport = 2
	}
	if b.MaxBridgeCandidates <= 0 {
		b.MaxBridgeCandidates = 200
	}
	if b.SimilarityEdgeThreshold <= 0 {
		// OpenAlex returns referenced_works_count=0 for most arxiv preprints,
		// which forces biblio=0 for whole subgraphs of modern ML/robotics
		// research. With (biblio+coCite)/2 averaging, that halves every
		// coCite-only pair and pushes legitimate bridges (e.g., pi_0 at
		// ~0.095 co-cite with Octo) below the original 0.15 cutoff. Dropping
		// to 0.08 restores the preprint cluster without opening the door to
		// incidental overlaps.
		b.SimilarityEdgeThreshold = 0.08
	}
	if b.MaxFirstHop <= 0 {
		b.MaxFirstHop = 300
	}
}

func (b *Builder) batchFetch(ctx context.Context, ids []string, fields []string) ([]citation.Paper, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]citation.Paper, 0, len(ids))
	for _, chunk := range chunkStrings(ids, b.BatchSize) {
		batch, err := b.S2.GetPaperBatch(ctx, chunk, fields)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

// refsBackfillBudget returns the per-build cap on paginated /references
// fallback calls. Zero picks the default (20); negative disables.
func (b *Builder) refsBackfillBudget() int {
	switch {
	case b.RefsBackfillBudget < 0:
		return 0
	case b.RefsBackfillBudget == 0:
		return 20
	default:
		return b.RefsBackfillBudget
	}
}

// recommendForSparseSeed asks the Recommender for topical neighbours
// when the seed's own refs+cites can't carry the 2-hop expansion. Empty
// when the recommender isn't wired, the seed is dense enough, the seed
// lacks a DOI/ArXiv id the recs endpoint can resolve, or the call errors.
// Errors are swallowed: a failed recommendation must not block the build.
func (b *Builder) recommendForSparseSeed(ctx context.Context, seed *citation.Paper) []string {
	if b.Recommender == nil {
		return nil
	}
	threshold := b.RecommendSparseThreshold
	if threshold == 0 {
		threshold = 10
	}
	if threshold < 0 {
		return nil
	}
	count := len(seed.References) + len(seed.Citations)
	if count >= threshold {
		return nil
	}
	lookupID := s2RecommendID(seed)
	if lookupID == "" {
		if b.Logger != nil {
			b.Logger.Info("recs: skipped, seed has no DOI/ArXiv id", "seed", seed.PaperID)
		}
		return nil
	}
	limit := b.RecommendLimit
	if limit <= 0 {
		limit = 30
	}
	recs, err := b.Recommender.Recommend(ctx, lookupID, limit, []string{"paperId", "externalIds", "title"})
	if err != nil {
		if b.Logger != nil {
			b.Logger.Warn("recs: lookup failed", "seed", seed.PaperID, "lookup", lookupID, "err", err)
		}
		return nil
	}
	out := make([]string, 0, len(recs))
	for _, p := range recs {
		if p.PaperID != "" {
			out = append(out, p.PaperID)
		}
	}
	if b.Logger != nil {
		b.Logger.Info("recs: sparse-seed augment", "seed", seed.PaperID, "lookup", lookupID, "raw", len(recs), "resolved", len(out), "seed_links", count, "threshold", threshold)
	}
	return out
}

// embeddingSimilarityEdges layers cosine-similarity edges over the
// already-built citation/biblio edges. For each pair of selected nodes
// that both expose a DOI or ArXiv id we ask the Embedder for specter_v2
// vectors and emit a similarity edge when cosine ≥ threshold. dedupeEdges
// later collapses any (kind, source, target) collision with the biblio
// edges, so this layer purely fills gaps where biblio coupling produced
// nothing — typically the recs cluster on a brand-new preprint seed.
//
// We send both "DOI:..." and "ARXIV:..." forms when applicable: many
// recent arxiv preprints surface in S2 under ARXIV id only, with empty
// externalIds.DOI, so a DOI-only query returns null.
func (b *Builder) embeddingSimilarityEdges(ctx context.Context, selectedIDs []string, links map[string]citation.Paper) []Edge {
	if b.Embedder == nil || len(selectedIDs) < 2 {
		return nil
	}
	threshold := b.EmbeddingSimilarityThreshold
	if threshold <= 0 {
		threshold = 0.7
	}
	topK := b.EmbeddingTopK
	if topK <= 0 {
		topK = 5
	}
	queriesByNode := make(map[string][]string, len(selectedIDs))
	allQueries := make([]string, 0, len(selectedIDs)*2)
	for _, id := range selectedIDs {
		p, ok := links[id]
		if !ok {
			continue
		}
		var qs []string
		doi := strings.ToLower(strings.TrimSpace(p.ExternalIDs["DOI"]))
		if doi != "" {
			qs = append(qs, "DOI:"+doi)
		}
		arxiv := strings.TrimSpace(p.ExternalIDs["ArXiv"])
		if arxiv == "" && strings.HasPrefix(doi, "10.48550/arxiv.") {
			arxiv = strings.TrimPrefix(doi, "10.48550/arxiv.")
		}
		if arxiv != "" {
			qs = append(qs, "ARXIV:"+arxiv)
		}
		if len(qs) == 0 {
			continue
		}
		queriesByNode[id] = qs
		allQueries = append(allQueries, qs...)
	}
	if len(allQueries) < 2 {
		return nil
	}
	embsByQuery, err := b.Embedder.EmbeddingsByExternalID(ctx, allQueries)
	if err != nil {
		if b.Logger != nil {
			b.Logger.Warn("embedder: lookup failed", "queried", len(allQueries), "err", err)
		}
		return nil
	}
	if len(embsByQuery) < 2 {
		if b.Logger != nil {
			b.Logger.Info("embedder: too few embeddings to wire similarity", "queried", len(allQueries), "received", len(embsByQuery))
		}
		return nil
	}
	nodeVec := func(id string) []float32 {
		for _, q := range queriesByNode[id] {
			if v, ok := embsByQuery[q]; ok && len(v) > 0 {
				return v
			}
		}
		return nil
	}
	type weighted struct {
		other string
		w     float64
	}
	neighbours := make(map[string][]weighted, len(selectedIDs))
	for i := 0; i < len(selectedIDs); i++ {
		vi := nodeVec(selectedIDs[i])
		if vi == nil {
			continue
		}
		for j := i + 1; j < len(selectedIDs); j++ {
			vj := nodeVec(selectedIDs[j])
			if vj == nil {
				continue
			}
			sim := cosineSim(vi, vj)
			if sim < threshold {
				continue
			}
			neighbours[selectedIDs[i]] = append(neighbours[selectedIDs[i]], weighted{other: selectedIDs[j], w: sim})
			neighbours[selectedIDs[j]] = append(neighbours[selectedIDs[j]], weighted{other: selectedIDs[i], w: sim})
		}
	}
	type edgeKey struct{ lo, hi string }
	keep := make(map[edgeKey]float64)
	for nodeID, ws := range neighbours {
		sort.Slice(ws, func(i, j int) bool { return ws[i].w > ws[j].w })
		limit := min(topK, len(ws))
		for i := 0; i < limit; i++ {
			lo, hi := nodeID, ws[i].other
			if lo > hi {
				lo, hi = hi, lo
			}
			keep[edgeKey{lo, hi}] = ws[i].w
		}
	}
	edges := make([]Edge, 0, len(keep))
	for k, w := range keep {
		edges = append(edges, Edge{Source: k.lo, Target: k.hi, Kind: EdgeSimilarity, Weight: w})
	}
	if b.Logger != nil {
		b.Logger.Info("embedder: similarity edges", "queried", len(allQueries), "received", len(embsByQuery), "edges", len(edges), "threshold", threshold, "topK", topK)
	}
	return edges
}

// cosineSim returns the cosine of the angle between a and b. Returns 0
// when either vector is empty or zero — caller treats below-threshold
// values as no-edge, so a 0 just means "skip".
func cosineSim(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, magA, magB float64
	for i, av := range a {
		bv := b[i]
		dot += float64(av) * float64(bv)
		magA += float64(av) * float64(av)
		magB += float64(bv) * float64(bv)
	}
	if magA == 0 || magB == 0 {
		return 0
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}

// s2RecommendID builds a S2-recommendable id from the seed. S2's
// /recommendations/v1/papers/forpaper/{id} accepts DOI: / ARXIV: prefixes
// alongside the bare 40-char hex paperId, so we try those external ids
// first and fall back to seed.PaperID when it already looks like an S2
// hex — under CITATION_PROVIDER=semanticscholar the seed comes back from
// S2 directly, sometimes without any DOI/ArXiv metadata, and sparse-seed
// recommendations would otherwise silently no-op. Returns "" when no
// usable id is present.
func s2RecommendID(seed *citation.Paper) string {
	if doi := strings.TrimSpace(seed.ExternalIDs["DOI"]); doi != "" {
		return "DOI:" + doi
	}
	if a := strings.TrimSpace(seed.ExternalIDs["ArXiv"]); a != "" {
		return "ARXIV:" + a
	}
	if isS2HexPaperID(seed.PaperID) {
		return seed.PaperID
	}
	return ""
}

// isS2HexPaperID reports whether id is a 40-character lowercase-hex
// string — the canonical Semantic Scholar paperId shape. We avoid using
// it on OpenAlex W-IDs (which start with "W") so the hybrid path doesn't
// mistakenly send a non-S2 id to /recommendations.
func isS2HexPaperID(id string) bool {
	if len(id) != 40 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// fetchSeed tries the cache first (requires refs/cites to be persisted)
// and falls back to S2 GetPaper with the full seedFields.
func (b *Builder) fetchSeed(ctx context.Context, seedID string) (*citation.Paper, error) {
	if b.Cache != nil {
		cached, err := b.Cache.GetPapersWithLinks(ctx, []string{seedID})
		if err == nil && len(cached) == 1 && cached[0].PaperID == seedID &&
			(len(cached[0].References) > 0 || len(cached[0].Citations) > 0) {
			p := cached[0]
			return &p, nil
		}
	}
	return b.S2.GetPaper(ctx, seedID, seedFields)
}

// fetchWithCache pulls as many papers as possible from the cache (papers
// whose links have been persisted) and only routes the remainder through
// the S2 batch endpoint.
func (b *Builder) fetchWithCache(ctx context.Context, ids []string, fields []string) ([]citation.Paper, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]citation.Paper, 0, len(ids))
	remaining := ids
	if b.Cache != nil {
		cached, err := b.Cache.GetPapersWithLinks(ctx, ids)
		if err == nil && len(cached) > 0 {
			have := make(map[string]struct{}, len(cached))
			for _, p := range cached {
				if len(p.References) > 0 || len(p.Citations) > 0 {
					out = append(out, p)
					have[p.PaperID] = struct{}{}
				}
			}
			if len(have) > 0 {
				remaining = make([]string, 0, len(ids)-len(have))
				for _, id := range ids {
					if _, ok := have[id]; !ok {
						remaining = append(remaining, id)
					}
				}
			}
		}
	}
	fetched, err := b.batchFetch(ctx, remaining, fields)
	if err != nil {
		return nil, err
	}
	return append(out, fetched...), nil
}

// persistFetched stores the freshly-fetched seed + full-node metadata and
// their ref/cite lists so subsequent builds (and /api/paper/{id} lookups)
// can be served from Postgres without hitting S2. Cache failures are
// swallowed — a slow cache must never break the request path.
func (b *Builder) persistFetched(ctx context.Context, seed *citation.Paper, fullNodes []citation.Paper) {
	if b.Cache == nil {
		return
	}
	papers := make([]citation.Paper, 0, 1+len(fullNodes))
	if seed != nil && seed.PaperID != "" {
		papers = append(papers, *seed)
	}
	for _, p := range fullNodes {
		if p.PaperID != "" && p.Title != "" {
			papers = append(papers, p)
		}
	}
	if len(papers) == 0 {
		return
	}
	_ = b.Cache.UpsertPapers(ctx, papers)
	for _, p := range papers {
		if len(p.References) == 0 && len(p.Citations) == 0 {
			continue
		}
		// The cache row is "complete" once links_fetched_at is stamped.
		// Skip persist when the provider couldn't supply cites — otherwise
		// a later GetPapersWithLinks read would hand the Builder an empty
		// cites slice as if the paper had zero citers, corrupting scoring.
		if p.CitationsUnknown {
			continue
		}
		_ = b.Cache.ReplacePaperLinks(ctx, p.PaperID, p.RefIDs(), p.CitedByIDs())
	}
}

// scoredCandidate keeps just the id and score so the "ranked" slice is cheap
// to sort and trim before we decide what to fully hydrate.
type scoredCandidate struct {
	id    string
	score float64
	// cc is the candidate's citation count when known, used as a deterministic
	// tiebreaker so equal scores preserve the "more-cited paper first" order.
	cc int
}

// rankCandidates scores every first-hop neighbor AND every hydrated 2-hop
// bridge on the Connected Papers scale: mean of bibliographic coupling and
// co-citation (Salton-normalized). The co-citation numerator is
// reconstructed from first-hop papers' refs — `|{P ∈ seed.citers : cand ∈
// P.refs}|` — so bridges and first-hop candidates are scored from the same
// data and a 2-hop bridge cited by many of the seed's citers (the pi_0 /
// Octo pattern) competes directly with weakly-coupled direct neighbors.
//
// Cached-cite fields on candidates are ignored by construction: only the
// seed's citers drive the numerator, and the denominator uses the provider's
// total CitationCount, which is stable across cache warm/cold runs.
func rankCandidates(
	seed *citation.Paper,
	firstHop []citation.Paper,
	bridges []citation.Paper,
) []scoredCandidate {
	seedRefs := seed.RefIDs()
	seedCiters := seed.CitedByIDs()
	// Normalized seed title is used to filter OpenAlex sibling Works that
	// share the seed's identity (Octo's RSS conference DOI W4402353985 and
	// its arxiv DOI W4398192846 are both indexed as separate Works; both
	// appear in the candidate pool via direct refs/citers and score ~0.99
	// against the seed, displacing genuine neighbors). Empty title leaves
	// the filter inactive — older test fixtures without titles still rank
	// unchanged.
	seedTitleNorm := citation.NormalizeTitle(seed.Title)

	// firstHopRefSets[P] is the set of ids P references. A subset of seedCiters
	// overlaps firstHop (seeds we fetched via minimalLinkFields), so we get
	// their refs here "for free" from the batch that was already paid for.
	firstHopRefSets := make(map[string]map[string]struct{}, len(firstHop))
	for _, p := range firstHop {
		refs := p.RefIDs()
		if p.PaperID == "" || len(refs) == 0 {
			continue
		}
		set := make(map[string]struct{}, len(refs))
		for _, r := range refs {
			set[r] = struct{}{}
		}
		firstHopRefSets[p.PaperID] = set
	}

	scores := make(map[string]scoredCandidate, len(firstHop)+len(bridges))

	scoreOne := func(p citation.Paper) {
		if p.PaperID == "" || p.PaperID == seed.PaperID {
			return
		}
		if _, already := scores[p.PaperID]; already {
			return
		}
		// Reject seed-aliased Works whose normalized title matches the
		// seed's. See seedTitleNorm comment above for the Octo regression.
		if seedTitleNorm != "" && p.Title != "" && citation.NormalizeTitle(p.Title) == seedTitleNorm {
			return
		}
		biblio := BibliographicCoupling(seedRefs, p.RefIDs())
		coCite := CoCitationApprox(seedCiters, firstHopRefSets, p.PaperID, seed.CitationCount, p.CitationCount)
		// rankingBonus combines a year-proximity lift (keeps the cluster
		// in the seed's generation) with a saturating log-citation lift
		// (rescues highly-cited refs that score low structurally). See
		// rankingBonus for component weights and tuning rationale.
		scores[p.PaperID] = scoredCandidate{
			id:    p.PaperID,
			score: ScoreCP(biblio, coCite) + rankingBonus(p.Year, seed.Year, p.CitationCount),
			cc:    p.CitationCount,
		}
	}

	for _, p := range firstHop {
		scoreOne(p)
	}
	for _, p := range bridges {
		scoreOne(p)
	}

	out := make([]scoredCandidate, 0, len(scores))
	for _, s := range scores {
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].cc != out[j].cc {
			return out[i].cc > out[j].cc
		}
		return out[i].id < out[j].id
	})
	return out
}

// selectBridgeIDs picks the top-`cap` 2-hop papers by support count, keeping
// only those meeting minSupport. Sorting by support (desc) then ID (asc for
// stability) makes the cut deterministic across runs for the same input.
func selectBridgeIDs(support map[string]int, minSupport, cap int) []string {
	type entry struct {
		id      string
		support int
	}
	candidates := make([]entry, 0, len(support))
	for id, s := range support {
		if s < minSupport {
			continue
		}
		candidates = append(candidates, entry{id: id, support: s})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].support != candidates[j].support {
			return candidates[i].support > candidates[j].support
		}
		return candidates[i].id < candidates[j].id
	})
	if cap > 0 && len(candidates) > cap {
		candidates = candidates[:cap]
	}
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.id)
	}
	return ids
}

// countTwoHopSupport counts, for every paper id reached by either a reference
// or a citation of some first-hop paper, how many distinct first-hop papers
// link to it. Own-first-hop members are excluded — we only want bridges.
func countTwoHopSupport(firstHop []citation.Paper, seedID string, firstHopByID map[string]citation.Paper) map[string]int {
	support := make(map[string]int)
	for _, p := range firstHop {
		seenFromP := make(map[string]struct{})
		tally := func(id string) {
			if id == "" || id == seedID {
				return
			}
			if _, inFirstHop := firstHopByID[id]; inFirstHop {
				return
			}
			if _, ok := seenFromP[id]; ok {
				return
			}
			seenFromP[id] = struct{}{}
			support[id]++
		}
		for _, rid := range p.RefIDs() {
			tally(rid)
		}
		for _, cid := range p.CitedByIDs() {
			tally(cid)
		}
	}
	return support
}

func buildCiteEdges(selectedIDs []string, selectedSet map[string]struct{}, links map[string]citation.Paper) []Edge {
	edges := make([]Edge, 0)
	for _, id := range selectedIDs {
		p, ok := links[id]
		if !ok {
			continue
		}
		for _, tid := range p.RefIDs() {
			if _, selected := selectedSet[tid]; !selected {
				continue
			}
			edges = append(edges, Edge{Source: id, Target: tid, Kind: EdgeCite, Weight: 1})
		}
	}
	return edges
}

// buildSimilarityEdges emits pairwise similarity edges for every unordered
// pair of selected nodes whose structural similarity (mean of bibliographic
// coupling and co-citation, each Salton-normalized) exceeds threshold.
// The edge is undirected in semantics; we canonicalize (lo, hi) so dedupe
// collapses the two orderings.
func buildSimilarityEdges(selectedIDs []string, links map[string]citation.Paper, threshold float64) []Edge {
	edges := make([]Edge, 0)
	for i := range selectedIDs {
		a, okA := links[selectedIDs[i]]
		if !okA {
			continue
		}
		aRefs := a.RefIDs()
		aCites := a.CitedByIDs()
		if len(aRefs) == 0 && len(aCites) == 0 {
			continue
		}
		for j := i + 1; j < len(selectedIDs); j++ {
			b, okB := links[selectedIDs[j]]
			if !okB {
				continue
			}
			biblio := BibliographicCoupling(aRefs, b.RefIDs())
			coCite := CoCitation(aCites, b.CitedByIDs())
			weight := (biblio + coCite) / 2
			if weight < threshold {
				continue
			}
			lo, hi := selectedIDs[i], selectedIDs[j]
			if lo > hi {
				lo, hi = hi, lo
			}
			edges = append(edges, Edge{Source: lo, Target: hi, Kind: EdgeSimilarity, Weight: weight})
		}
	}
	return edges
}

func indexPapers(ps []citation.Paper) map[string]citation.Paper {
	out := make(map[string]citation.Paper, len(ps))
	for _, p := range ps {
		if p.PaperID != "" {
			out[p.PaperID] = p
		}
	}
	return out
}

func sliceSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func dedupeExcluding(ids []string, exclude ...string) []string {
	excl := make(map[string]struct{}, len(exclude))
	for _, e := range exclude {
		if e != "" {
			excl[e] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, skip := excl[id]; skip {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func filterOut(ids []string, drop string) []string {
	if drop == "" {
		return ids
	}
	out := ids[:0]
	for _, id := range ids {
		if id != drop {
			out = append(out, id)
		}
	}
	return out
}

// canonicalizeSeedAlias rewrites every occurrence of the seed's secondary
// paperId (e.g. Semantic Scholar hex when the primary provider is OpenAlex)
// to the canonical primary paperId inside the papers' refs/cites lists.
// This collapses cross-provider references so the seed node in the final
// graph absorbs cite edges coming from papers that were fetched from the
// secondary provider.
func canonicalizeSeedAlias(papers []citation.Paper, aliasID, canonicalID string) {
	if aliasID == "" || canonicalID == "" || aliasID == canonicalID {
		return
	}
	for i := range papers {
		for j := range papers[i].References {
			if papers[i].References[j].PaperID == aliasID {
				papers[i].References[j].PaperID = canonicalID
			}
		}
		for j := range papers[i].Citations {
			if papers[i].Citations[j].PaperID == aliasID {
				papers[i].Citations[j].PaperID = canonicalID
			}
		}
	}
}

func chunkStrings(in []string, size int) [][]string {
	if size <= 0 {
		return [][]string{in}
	}
	out := make([][]string, 0, (len(in)+size-1)/size)
	for i := 0; i < len(in); i += size {
		out = append(out, in[i:min(i+size, len(in))])
	}
	return out
}

func dedupeEdges(edges []Edge) []Edge {
	seen := make(map[[3]string]struct{}, len(edges))
	out := edges[:0]
	for _, e := range edges {
		key := [3]string{string(e.Kind), e.Source, e.Target}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return slices.Clone(out)
}
