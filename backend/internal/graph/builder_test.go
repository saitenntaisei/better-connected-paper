package graph

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/saitenntaisei/better-connected-paper/internal/citation"
)

// stubS2 records every GetPaperBatch call so tests can assert the builder is
// not paying for full metadata on the entire first-hop cloud before pruning.
type stubS2 struct {
	papers map[string]citation.Paper
	// calls is append-only; each entry is the field list passed to GetPaperBatch
	// alongside the ids requested. Order preserved.
	calls []batchCall
}

type batchCall struct {
	fields []string
	ids    []string
}

func (s *stubS2) GetPaper(_ context.Context, id string, _ []string) (*citation.Paper, error) {
	p, ok := s.papers[id]
	if !ok {
		return nil, citation.ErrNotFound
	}
	return &p, nil
}

func (s *stubS2) GetPaperBatch(_ context.Context, ids []string, fields []string) ([]citation.Paper, error) {
	s.calls = append(s.calls, batchCall{fields: slices.Clone(fields), ids: slices.Clone(ids)})
	out := make([]citation.Paper, 0, len(ids))
	for _, id := range ids {
		if p, ok := s.papers[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func paper(id string, refs, cites []string) citation.Paper {
	p := citation.Paper{PaperID: id, Title: "paper-" + id, Year: 2020}
	for _, r := range refs {
		p.References = append(p.References, citation.Paper{PaperID: r})
	}
	for _, c := range cites {
		p.Citations = append(p.Citations, citation.Paper{PaperID: c})
	}
	return p
}

func edgeKey(e Edge) [3]string {
	return [3]string{string(e.Kind), e.Source, e.Target}
}

// Scenario:
//
//	seed S references R1, R2; is cited by C1, C2.
//	R1 references X (2-hop); R1 also cited by C1 (co-citation with S).
//	R2 references Y (2-hop).
//	C1 references S, R1 (so direct + co-citation with R1).
//	C2 references S only.
func TestBuildDirectedEdgesAndSimilarity(t *testing.T) {
	s2 := &stubS2{papers: map[string]citation.Paper{
		"S":  paper("S", []string{"R1", "R2"}, []string{"C1", "C2"}),
		"R1": paper("R1", []string{"X"}, []string{"C1"}),
		"R2": paper("R2", []string{"Y"}, nil),
		"C1": paper("C1", []string{"S", "R1"}, nil),
		"C2": paper("C2", []string{"S"}, nil),
	}}
	b := &Builder{S2: s2, MaxNodes: 10, SimilarityEdgeThreshold: 0.0001}

	resp, err := b.Build(context.Background(), "S")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if resp.Seed.ID != "S" || !resp.Seed.IsSeed {
		t.Fatalf("bad seed: %+v", resp.Seed)
	}

	ids := map[string]Node{}
	for _, n := range resp.Nodes {
		ids[n.ID] = n
	}
	for _, want := range []string{"S", "R1", "R2", "C1", "C2"} {
		if _, ok := ids[want]; !ok {
			t.Errorf("missing node %s", want)
		}
	}

	if ids["S"].Similarity != 0 {
		t.Errorf("seed similarity should be 0, got %v", ids["S"].Similarity)
	}
	for _, id := range []string{"R1", "R2", "C1", "C2"} {
		if ids[id].Similarity < WeightDirectLink {
			t.Errorf("%s similarity = %v, want >= %v (direct)", id, ids[id].Similarity, WeightDirectLink)
		}
	}
	if ids["C1"].Similarity <= ids["C2"].Similarity {
		t.Errorf("expected C1 (biblio-couples with seed via R1) > C2; got C1=%v C2=%v",
			ids["C1"].Similarity, ids["C2"].Similarity)
	}

	// Split edges by kind; assert on directed cites explicitly.
	citeEdges := map[[2]string]bool{}
	simEdges := map[[2]string]float64{}
	for _, e := range resp.Edges {
		switch e.Kind {
		case EdgeCite:
			citeEdges[[2]string{e.Source, e.Target}] = true
		case EdgeSimilarity:
			simEdges[[2]string{e.Source, e.Target}] = e.Weight
		default:
			t.Errorf("unexpected edge kind %s", e.Kind)
		}
	}
	wantCites := [][2]string{
		{"S", "R1"},
		{"S", "R2"},
		{"C1", "S"},
		{"C1", "R1"},
		{"C2", "S"},
	}
	for _, key := range wantCites {
		if !citeEdges[key] {
			t.Errorf("missing cite edge %s -> %s", key[0], key[1])
		}
	}
	if citeEdges[[2]string{"R1", "X"}] {
		t.Error("cite edge R1 -> X leaked into graph; X is outside selected set")
	}
	if len(simEdges) == 0 {
		t.Error("expected at least one similarity edge; got none")
	}
}

// Second-hop bridge discovery: candidate P is not directly linked to S but
// is referenced by multiple first-hop papers, so it must surface as a node.
func TestBuildIncludesTwoHopBridges(t *testing.T) {
	s2 := &stubS2{papers: map[string]citation.Paper{
		"S":  paper("S", []string{"R1", "R2", "R3"}, nil),
		"R1": paper("R1", []string{"B"}, nil),
		"R2": paper("R2", []string{"B"}, nil),
		"R3": paper("R3", []string{"B"}, nil),
		"B":  paper("B", nil, nil),
	}}
	b := &Builder{S2: s2, MaxNodes: 10, TwoHopSupport: 2}

	resp, err := b.Build(context.Background(), "S")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	found := false
	for _, n := range resp.Nodes {
		if n.ID == "B" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected two-hop bridge B to appear in nodes; missing")
	}
}

func TestBuildRespectsMaxNodes(t *testing.T) {
	refs := []string{}
	papers := map[string]citation.Paper{}
	for i := range 20 {
		id := string(rune('a' + i))
		refs = append(refs, id)
		papers[id] = paper(id, nil, nil)
	}
	papers["S"] = paper("S", refs, nil)
	b := &Builder{S2: &stubS2{papers: papers}, MaxNodes: 5}

	resp, err := b.Build(context.Background(), "S")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := len(resp.Nodes); got != 5 {
		t.Errorf("want 5 nodes (seed + 4), got %d", got)
	}
}

// Build must fetch full metadata ONLY for the selected subset, not for the
// entire first-hop cloud. A first-hop cloud of 20 with MaxNodes=5 means at
// most 4 ids hit fullNodeFields; the 20-wide fetch must use minimalLinkFields.
func TestBuildBoundsFullMetadataFanOut(t *testing.T) {
	refs := []string{}
	papers := map[string]citation.Paper{}
	for i := range 20 {
		id := string(rune('a' + i))
		refs = append(refs, id)
		papers[id] = paper(id, nil, nil)
	}
	papers["S"] = paper("S", refs, nil)
	s2 := &stubS2{papers: papers}
	b := &Builder{S2: s2, MaxNodes: 5, BatchSize: 100}

	if _, err := b.Build(context.Background(), "S"); err != nil {
		t.Fatalf("build: %v", err)
	}

	var minimal, full *batchCall
	for i := range s2.calls {
		c := &s2.calls[i]
		switch {
		case reflect.DeepEqual(c.fields, minimalLinkFields):
			minimal = c
		case reflect.DeepEqual(c.fields, fullNodeFields):
			full = c
		}
	}
	if minimal == nil {
		t.Fatal("expected a minimalLinkFields batch call; got none")
	}
	if full == nil {
		t.Fatal("expected a fullNodeFields batch call; got none")
	}
	if len(minimal.ids) != 20 {
		t.Errorf("minimal batch ids = %d, want 20 (full first-hop)", len(minimal.ids))
	}
	if want := 4; len(full.ids) != want {
		t.Errorf("full-metadata batch ids = %d, want %d (MaxNodes-1 after pruning)", len(full.ids), want)
	}
}

// Similarity edges must appear when two selected candidates share references,
// not only when the seed directly bridges them. This verifies the pairwise
// pass isn't limited to seed-vs-candidate.
func TestBuildEmitsSimilarityEdgesBetweenCandidates(t *testing.T) {
	// R1 and R2 share two references (X, Y); both are referenced by seed.
	s2 := &stubS2{papers: map[string]citation.Paper{
		"S":  paper("S", []string{"R1", "R2"}, nil),
		"R1": paper("R1", []string{"X", "Y"}, nil),
		"R2": paper("R2", []string{"X", "Y"}, nil),
	}}
	b := &Builder{S2: s2, MaxNodes: 10, SimilarityEdgeThreshold: 0.1}

	resp, err := b.Build(context.Background(), "S")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	found := false
	for _, e := range resp.Edges {
		if e.Kind != EdgeSimilarity {
			continue
		}
		pair := [2]string{e.Source, e.Target}
		if pair == [2]string{"R1", "R2"} || pair == [2]string{"R2", "R1"} {
			found = true
			if e.Weight <= 0 {
				t.Errorf("similarity edge weight = %v, want > 0", e.Weight)
			}
		}
	}
	if !found {
		t.Error("expected R1-R2 similarity edge; none emitted")
	}
}

func TestBuildSetsBuiltAt(t *testing.T) {
	stamp := time.Date(2026, 4, 18, 9, 0, 0, 0, time.UTC)
	papers := map[string]citation.Paper{"S": paper("S", nil, nil)}
	b := &Builder{S2: &stubS2{papers: papers}, Now: func() time.Time { return stamp }}
	resp, err := b.Build(context.Background(), "S")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !resp.BuiltAt.Equal(stamp) {
		t.Errorf("got %v, want %v", resp.BuiltAt, stamp)
	}
}

// Guard against Edge dedupe collapsing a cite+similarity pair that happens to
// share endpoints but not kind.
func TestDedupeEdgesKeepsDifferentKinds(t *testing.T) {
	edges := []Edge{
		{Source: "A", Target: "B", Kind: EdgeCite, Weight: 1},
		{Source: "A", Target: "B", Kind: EdgeSimilarity, Weight: 0.5},
		{Source: "A", Target: "B", Kind: EdgeCite, Weight: 1}, // exact dup
	}
	got := dedupeEdges(edges)
	if len(got) != 2 {
		t.Fatalf("dedupeEdges returned %d edges, want 2 (one per kind); got %+v", len(got), got)
	}
	seen := map[EdgeKind]bool{}
	for _, e := range got {
		seen[e.Kind] = true
	}
	if !seen[EdgeCite] || !seen[EdgeSimilarity] {
		t.Errorf("expected both EdgeCite and EdgeSimilarity preserved; got %+v", seen)
	}
}

// Avoid edgeKey lint noise in environments without it: force reference.
var _ = edgeKey(Edge{})
