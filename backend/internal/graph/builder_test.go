package graph

import (
	"context"
	"testing"
	"time"

	"github.com/saitenntaisei/better-connected-paper/internal/citation"
)

type stubS2 struct {
	papers map[string]citation.Paper
}

func (s *stubS2) GetPaper(_ context.Context, id string, _ []string) (*citation.Paper, error) {
	p, ok := s.papers[id]
	if !ok {
		return nil, citation.ErrNotFound
	}
	return &p, nil
}

func (s *stubS2) GetPaperBatch(_ context.Context, ids []string, _ []string) ([]citation.Paper, error) {
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

// Build scenario:
//
//	seed S references R1, R2; is cited by C1, C2.
//	R1 references X (shared with S? no, X is unrelated). R1 also cited by C1 (co-citation with S).
//	R2 references nothing shared.
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
	b := &Builder{S2: s2, MaxNodes: 10}

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

	// Seed must have similarity = 0
	if ids["S"].Similarity != 0 {
		t.Errorf("seed similarity should be 0, got %v", ids["S"].Similarity)
	}

	// Direct-linked candidates (R1, R2, C1, C2) all receive WeightDirectLink at minimum.
	for _, id := range []string{"R1", "R2", "C1", "C2"} {
		if ids[id].Similarity < WeightDirectLink {
			t.Errorf("%s similarity = %v, want >= %v (direct)", id, ids[id].Similarity, WeightDirectLink)
		}
	}

	// C1 should score higher than C2 because C1 has reference overlap with seed (shares R1).
	if ids["C1"].Similarity <= ids["C2"].Similarity {
		t.Errorf("expected C1 (biblio-couples with seed via R1) > C2; got C1=%v C2=%v",
			ids["C1"].Similarity, ids["C2"].Similarity)
	}

	// Edges should include: S->R1, S->R2, C1->S, C1->R1, C2->S.
	wantDirected := map[[2]string]bool{
		{"S", "R1"}:  true,
		{"S", "R2"}:  true,
		{"C1", "S"}:  true,
		{"C1", "R1"}: true,
		{"C2", "S"}:  true,
	}
	gotDirected := make(map[[2]string]bool, len(resp.Edges))
	for _, e := range resp.Edges {
		if e.Kind != EdgeCite {
			t.Errorf("unexpected edge kind %s", e.Kind)
		}
		gotDirected[[2]string{e.Source, e.Target}] = true
	}
	for key := range wantDirected {
		if !gotDirected[key] {
			t.Errorf("missing edge %s -> %s", key[0], key[1])
		}
	}
	// R1->X shouldn't exist because X isn't a selected node.
	if gotDirected[[2]string{"R1", "X"}] {
		t.Error("edge R1 -> X leaked into graph; X is outside selected set")
	}
}

func TestBuildRespectsMaxNodes(t *testing.T) {
	// Seed references 20 papers. MaxNodes = 5 means 4 candidates + seed.
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
