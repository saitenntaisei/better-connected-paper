package graph

import (
	"context"
	"fmt"
	"slices"
	"sort"
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
}

// Builder constructs the directed graph around a seed paper.
type Builder struct {
	S2        S2
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
// paper id plus the id lists we need for scoring. No title/abstract/etc.,
// so the response size stays linear in |refs|+|cites| rather than quadratic.
// citationCount is required by CoCitationApprox as the Salton denominator
// scale for each candidate; without it the coCite term collapses to 0.
var minimalLinkFields = []string{
	"paperId",
	"citationCount",
	"references.paperId",
	"citations.paperId",
}

// bridgeLinkFields is the refs-only fetch used for 2-hop bridge candidates.
// Cites enrichment is deliberately skipped: most famous bridges have >100
// citers, the OpenAlex client would then flag them CitationsUnknown, and
// we'd have paid a per-paper fanout of cites requests for nothing. Refs are
// returned inline in the /works response, so this batch stays ~O(|bridges|).
// citationCount is required for coCite denominator; see minimalLinkFields.
var bridgeLinkFields = []string{
	"paperId",
	"citationCount",
	"references.paperId",
}

// fullNodeFields hydrates a selected node for the final response (title,
// authors, abstract, etc.). We only pay this for at most MaxNodes papers.
var fullNodeFields = []string{
	"paperId", "title", "abstract", "year", "venue", "authors",
	"citationCount", "referenceCount", "externalIds", "url",
	"references.paperId",
	"citations.paperId",
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
	firstHopIDs := dedupeExcluding(append(append([]string(nil), seedRefs...), seedCitedBy...), excluded...)
	if b.MaxFirstHop > 0 && len(firstHopIDs) > b.MaxFirstHop {
		firstHopIDs = firstHopIDs[:b.MaxFirstHop]
	}
	firstHop, err := b.fetchWithCache(ctx, firstHopIDs, minimalLinkFields)
	if err != nil {
		return nil, fmt.Errorf("fetch first hop: %w", err)
	}
	canonicalizeSeedAlias(firstHop, seedAlias, seed.PaperID)
	firstHopByID := indexPapers(firstHop)

	twoHopSupport := countTwoHopSupport(firstHop, seed.PaperID, firstHopByID)

	// Bridge candidates: 2-hop papers with enough first-hop support to be
	// worth hydrating. Capped to avoid a blow-out for high-degree seeds.
	bridgeIDs := selectBridgeIDs(twoHopSupport, b.TwoHopSupport, b.MaxBridgeCandidates)
	if seedAlias != "" {
		bridgeIDs = filterOut(bridgeIDs, seedAlias)
	}
	bridges, err := b.fetchWithCache(ctx, bridgeIDs, bridgeLinkFields)
	if err != nil {
		return nil, fmt.Errorf("fetch two-hop bridges: %w", err)
	}
	canonicalizeSeedAlias(bridges, seedAlias, seed.PaperID)
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

	full, err := b.fetchWithCache(ctx, selectedIDs[1:], fullNodeFields)
	if err != nil {
		return nil, fmt.Errorf("fetch selected metadata: %w", err)
	}
	canonicalizeSeedAlias(full, seedAlias, seed.PaperID)
	fullByID := indexPapers(full)
	b.persistFetched(ctx, seed, full)

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
		n.Similarity = s.score
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
	edges = dedupeEdges(edges)

	nodes, edges = pruneOrphanNodes(seedNode.ID, nodes, edges)

	return &Response{
		Seed:    seedNode,
		Nodes:   nodes,
		Edges:   edges,
		BuiltAt: now().UTC(),
	}, nil
}

// pruneOrphanNodes drops non-seed nodes that participate in zero edges. Such
// nodes score high enough on seed-only signals to make top-N but share no
// structural overlap with any other selected node, so Cytoscape layouts stack
// them at the origin and the visualization reads worse than a smaller but
// fully-connected graph. The seed is preserved unconditionally.
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
		biblio := BibliographicCoupling(seedRefs, p.RefIDs())
		coCite := CoCitationApprox(seedCiters, firstHopRefSets, p.PaperID, seed.CitationCount, p.CitationCount)
		scores[p.PaperID] = scoredCandidate{
			id:    p.PaperID,
			score: ScoreCP(biblio, coCite),
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
		if out[i].score == out[j].score {
			return out[i].cc > out[j].cc
		}
		return out[i].score > out[j].score
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
