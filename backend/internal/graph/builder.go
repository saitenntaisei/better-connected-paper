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

// Builder constructs the directed graph around a seed paper.
type Builder struct {
	S2        S2
	MaxNodes  int           // default 40
	BatchSize int           // S2 batch cap: 500, we use 100 to keep responses small
	Timeout   time.Duration // default 90s
	Now       func() time.Time

	// TwoHopSupport is the minimum number of first-hop papers a second-hop
	// candidate must connect to before we consider it. 2 means "shared by at
	// least two first-hop neighbors", which is the standard bibliographic-
	// coupling signal for bridge papers. Default 2.
	TwoHopSupport int

	// SimilarityEdgeThreshold is the minimum pairwise similarity weight
	// required to emit a similarity edge in the returned graph. Default 0.15.
	SimilarityEdgeThreshold float64
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
var minimalLinkFields = []string{
	"paperId",
	"references.paperId",
	"citations.paperId",
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

	seed, err := b.S2.GetPaper(ctx, seedID, seedFields)
	if err != nil {
		return nil, fmt.Errorf("fetch seed: %w", err)
	}
	if seed.PaperID == "" {
		return nil, fmt.Errorf("seed %q returned no paperId", seedID)
	}

	seedRefs := seed.RefIDs()
	seedCitedBy := seed.CitedByIDs()

	firstHopIDs := dedupeExcluding(append(append([]string(nil), seedRefs...), seedCitedBy...), seed.PaperID)
	firstHop, err := b.batchFetch(ctx, firstHopIDs, minimalLinkFields)
	if err != nil {
		return nil, fmt.Errorf("fetch first hop: %w", err)
	}
	firstHopByID := indexPapers(firstHop)

	twoHopSupport := countTwoHopSupport(firstHop, seed.PaperID, firstHopByID)
	scored := rankCandidates(seed, firstHop, twoHopSupport, b.TwoHopSupport)
	if len(scored) > b.MaxNodes-1 {
		scored = scored[:b.MaxNodes-1]
	}

	selectedIDs := make([]string, 0, len(scored)+1)
	selectedIDs = append(selectedIDs, seed.PaperID)
	for _, s := range scored {
		selectedIDs = append(selectedIDs, s.id)
	}

	full, err := b.batchFetch(ctx, selectedIDs[1:], fullNodeFields)
	if err != nil {
		return nil, fmt.Errorf("fetch selected metadata: %w", err)
	}
	fullByID := indexPapers(full)

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
		}
	}

	selectedSet := sliceSet(selectedIDs)
	edges := buildCiteEdges(selectedIDs, selectedSet, linkSource)
	edges = append(edges, buildSimilarityEdges(selectedIDs, linkSource, b.SimilarityEdgeThreshold)...)
	edges = dedupeEdges(edges)

	return &Response{
		Seed:    seedNode,
		Nodes:   nodes,
		Edges:   edges,
		BuiltAt: now().UTC(),
	}, nil
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
	if b.SimilarityEdgeThreshold <= 0 {
		b.SimilarityEdgeThreshold = 0.15
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

// scoredCandidate keeps just the id and score so the "ranked" slice is cheap
// to sort and trim before we decide what to fully hydrate.
type scoredCandidate struct {
	id    string
	score float64
	// cc is the candidate's citation count when known, used as a deterministic
	// tiebreaker so equal scores preserve the "more-cited paper first" order.
	cc int
}

// rankCandidates assigns a score to every first-hop paper (biblio coupling +
// co-citation + direct link to seed) and to every second-hop bridge (fraction
// of first-hop papers that link to it, weighted). 2-hop candidates can't be
// biblio-coupled against seed because we haven't fetched their refs; using
// their connectivity to the first-hop cloud is the structural proxy.
func rankCandidates(
	seed *citation.Paper,
	firstHop []citation.Paper,
	twoHopSupport map[string]int,
	minSupport int,
) []scoredCandidate {
	seedRefs := seed.RefIDs()
	seedCitedBy := seed.CitedByIDs()

	scores := make(map[string]scoredCandidate, len(firstHop)+len(twoHopSupport))

	for _, p := range firstHop {
		if p.PaperID == "" || p.PaperID == seed.PaperID {
			continue
		}
		biblio := BibliographicCoupling(seedRefs, p.RefIDs())
		coCite := CoCitation(seedCitedBy, p.CitedByIDs())
		direct := DirectLink(seedRefs, seedCitedBy, p.PaperID)
		scores[p.PaperID] = scoredCandidate{
			id:    p.PaperID,
			score: Score(biblio, coCite, direct),
			cc:    p.CitationCount,
		}
	}

	if len(firstHop) > 0 {
		for id, support := range twoHopSupport {
			if support < minSupport {
				continue
			}
			if _, already := scores[id]; already {
				continue
			}
			if id == seed.PaperID {
				continue
			}
			// Normalize to [0,1]: fully-supported = 1.0 (every first-hop
			// paper bridges to this candidate). Multiplied by 0.5 so a
			// 2-hop bridge with maximal support (1.0) scores 0.5 — below
			// a well-coupled first-hop paper but above an unrelated one.
			ratio := float64(support) / float64(len(firstHop))
			scores[id] = scoredCandidate{id: id, score: 0.5 * ratio}
		}
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

func dedupeExcluding(ids []string, exclude string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || id == exclude {
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
