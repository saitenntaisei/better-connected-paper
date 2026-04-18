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

// candidateFields fetches minimal metadata + nested ids for each candidate.
var candidateFields = []string{
	"paperId", "title", "abstract", "year", "venue", "authors",
	"citationCount", "referenceCount", "externalIds", "url",
	"references.paperId",
	"citations.paperId",
}

// Build expands the graph around seedID and returns the response ready for the frontend.
func (b *Builder) Build(ctx context.Context, seedID string) (*Response, error) {
	if b.MaxNodes <= 0 {
		b.MaxNodes = 40
	}
	if b.BatchSize <= 0 {
		b.BatchSize = 100
	}
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

	candidateIDs := dedupeExcluding(append(append([]string(nil), seedRefs...), seedCitedBy...), seed.PaperID)
	candidates := []citation.Paper{}
	if len(candidateIDs) > 0 {
		for _, chunk := range chunkStrings(candidateIDs, b.BatchSize) {
			batch, err := b.S2.GetPaperBatch(ctx, chunk, candidateFields)
			if err != nil {
				return nil, fmt.Errorf("fetch candidates: %w", err)
			}
			candidates = append(candidates, batch...)
		}
	}

	type scored struct {
		p     citation.Paper
		score float64
	}
	scoredCandidates := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		if c.PaperID == "" || c.PaperID == seed.PaperID {
			continue
		}
		biblio := BibliographicCoupling(seedRefs, c.RefIDs())
		coCite := CoCitation(seedCitedBy, c.CitedByIDs())
		direct := DirectLink(seedRefs, seedCitedBy, c.PaperID)
		scoredCandidates = append(scoredCandidates, scored{c, Score(biblio, coCite, direct)})
	}

	sort.SliceStable(scoredCandidates, func(i, j int) bool {
		if scoredCandidates[i].score == scoredCandidates[j].score {
			return scoredCandidates[i].p.CitationCount > scoredCandidates[j].p.CitationCount
		}
		return scoredCandidates[i].score > scoredCandidates[j].score
	})

	limit := min(b.MaxNodes-1, len(scoredCandidates))
	selected := scoredCandidates[:max(0, limit)]

	selectedIDs := make(map[string]struct{}, len(selected)+1)
	selectedIDs[seed.PaperID] = struct{}{}
	for _, s := range selected {
		selectedIDs[s.p.PaperID] = struct{}{}
	}

	seedNode := ToNode(*seed)
	seedNode.IsSeed = true
	seedNode.Similarity = 0

	nodes := make([]Node, 0, len(selected)+1)
	nodes = append(nodes, seedNode)
	for _, s := range selected {
		n := ToNode(s.p)
		n.Similarity = s.score
		nodes = append(nodes, n)
	}

	// Directed citation edges: for every paper P in the selected set (seed + candidates),
	// for each paperId R in P.References, if R is also selected, add edge P -> R.
	edges := []Edge{}
	addEdges := func(src citation.Paper) {
		for _, tid := range src.RefIDs() {
			if _, ok := selectedIDs[tid]; !ok {
				continue
			}
			edges = append(edges, Edge{Source: src.PaperID, Target: tid, Kind: EdgeCite, Weight: 1})
		}
	}
	addEdges(*seed)
	for _, s := range selected {
		addEdges(s.p)
	}
	edges = dedupeEdges(edges)

	return &Response{
		Seed:    seedNode,
		Nodes:   nodes,
		Edges:   edges,
		BuiltAt: now().UTC(),
	}, nil
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
	seen := make(map[[2]string]struct{}, len(edges))
	out := edges[:0]
	for _, e := range edges {
		key := [2]string{e.Source, e.Target}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return slices.Clone(out)
}
