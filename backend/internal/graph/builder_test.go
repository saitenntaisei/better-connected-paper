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
	// Under the CP-faithful formula a direct neighbor with no structural
	// overlap scores 0; we only require the structurally-richer citer (C1)
	// to outrank C2 (biblio=0, coCite=0).
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

// A bridge's similarity score must not depend on whether its citers happen
// to be populated from a previous cache hit. bridgeLinkFields intentionally
// skips cites to avoid the per-paper cites fanout, but the cache can still
// hand back a bridge with cites from a prior fullNodeFields run. Scoring
// must ignore those cites so a given (seed, first-hop) pair always yields
// the same ranking.
func TestBuildBridgeRankingIgnoresCachedCites(t *testing.T) {
	// Seed S is cited by C1, C2 (so seed.citers = {C1, C2}).
	// R1, R2 are direct refs; both also cite B (the bridge).
	// Variant A: B has no cites populated (cold cache).
	// Variant B: B was previously selected and cached with cites = {C1}, so
	// on re-entry GetPapersWithLinks would return B with C1 in citers. Under
	// naive scoring, Variant B would give B a non-zero CoCitation term.
	basePapers := func(bCites []string) map[string]citation.Paper {
		return map[string]citation.Paper{
			"S":  paper("S", []string{"R1", "R2"}, []string{"C1", "C2"}),
			"R1": paper("R1", []string{"B"}, nil),
			"R2": paper("R2", []string{"B"}, nil),
			"C1": paper("C1", nil, nil),
			"C2": paper("C2", nil, nil),
			"B":  paper("B", nil, bCites),
		}
	}

	run := func(bCites []string) float64 {
		// Pre-stage the bridge in the cache with the given cites so
		// fetchWithCache returns it without touching S2.
		cache := &stubCache{have: map[string]citation.Paper{
			"B": paper("B", nil, bCites),
		}}
		s2 := &stubS2{papers: basePapers(bCites)}
		b := &Builder{S2: s2, Cache: cache, MaxNodes: 10, TwoHopSupport: 2, SimilarityEdgeThreshold: 0.9}
		resp, err := b.Build(context.Background(), "S")
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		for _, n := range resp.Nodes {
			if n.ID == "B" {
				return n.Similarity
			}
		}
		t.Fatal("bridge B missing from nodes")
		return 0
	}

	cold := run(nil)
	warm := run([]string{"C1"})
	if cold != warm {
		t.Errorf("bridge similarity changed across rebuilds: cold=%v warm=%v", cold, warm)
	}
}

// fieldAwareStubS2 only populates CitationCount when the "citationCount"
// field is explicitly requested — mirroring Semantic Scholar's behaviour
// where response content is gated by the fields= query param. This lets
// tests assert that the Builder's field lists actually request the data
// CoCitationApprox needs for its Salton denominator.
type fieldAwareStubS2 struct {
	papers map[string]citation.Paper
}

func (s *fieldAwareStubS2) trim(ids []string, fields []string) []citation.Paper {
	wantCC := slices.Contains(fields, "citationCount")
	out := make([]citation.Paper, 0, len(ids))
	for _, id := range ids {
		p, ok := s.papers[id]
		if !ok {
			continue
		}
		if !wantCC {
			p.CitationCount = 0
		}
		out = append(out, p)
	}
	return out
}

func (s *fieldAwareStubS2) GetPaper(_ context.Context, id string, fields []string) (*citation.Paper, error) {
	ps := s.trim([]string{id}, fields)
	if len(ps) == 0 {
		return nil, citation.ErrNotFound
	}
	return &ps[0], nil
}

func (s *fieldAwareStubS2) GetPaperBatch(_ context.Context, ids []string, fields []string) ([]citation.Paper, error) {
	return s.trim(ids, fields), nil
}

// Direct assertion: the field lists used for scoring-time fetches must
// explicitly request citationCount — CoCitationApprox uses it as the Salton
// denominator and guards on candCitationTotal <= 0, so a silent drop from
// either list makes coCite collapse to zero everywhere. Guards against
// future edits that reshuffle field constants without realising scoring
// depends on this field.
func TestScoringFieldListsIncludeCitationCount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []string
	}{
		{"minimalLinkFields", minimalLinkFields},
		{"bridgeLinkFields", bridgeLinkFields},
	} {
		if !slices.Contains(tc.fields, "citationCount") {
			t.Errorf("%s missing %q — CoCitationApprox needs it as the Salton denominator for candidate citers", tc.name, "citationCount")
		}
	}
}

// Regression: the minimal- and bridge-fetch field lists MUST include
// citationCount. Without it, CoCitationApprox collapses to 0 (it guards on
// candCitationTotal <= 0) and any co-citation contribution to the score
// silently disappears. Verified by driving the build through a stub that
// only emits CitationCount when the field is requested, then demanding a
// pure co-citation bridge surfaces with score > biblio-only (i.e., non-zero
// coCite actually contributed).
func TestBuildFieldListsRequestCitationCount(t *testing.T) {
	// Seed S has 3 citers; all three cite bridge B. B has no refs overlap
	// with S, so biblio(S, B) = 0. If coCite is active, B surfaces with
	// score > 0; if citationCount was missing from the minimal-fetch field
	// list, CoCitationApprox returns 0 and B scores 0.
	withCitationCount := func(p citation.Paper, cc int) citation.Paper {
		p.CitationCount = cc
		return p
	}
	seed := withCitationCount(paper("S", nil, []string{"C1", "C2", "C3"}), 3)
	bridge := withCitationCount(paper("B", nil, nil), 3)
	s2 := &fieldAwareStubS2{papers: map[string]citation.Paper{
		"S":  seed,
		"C1": withCitationCount(paper("C1", []string{"S", "B"}, nil), 0),
		"C2": withCitationCount(paper("C2", []string{"S", "B"}, nil), 0),
		"C3": withCitationCount(paper("C3", []string{"S", "B"}, nil), 0),
		"B":  bridge,
	}}
	b := &Builder{S2: s2, MaxNodes: 10, TwoHopSupport: 2, SimilarityEdgeThreshold: 0.9}

	resp, err := b.Build(context.Background(), "S")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	var sim float64
	found := false
	for _, n := range resp.Nodes {
		if n.ID == "B" {
			found = true
			sim = n.Similarity
		}
	}
	if !found {
		t.Fatal("bridge B missing — did minimalLinkFields/bridgeLinkFields drop citationCount?")
	}
	if sim <= 0 {
		t.Errorf("bridge B similarity = %v, want > 0 (coCite must survive field-gating)", sim)
	}
}

// The pi_0 / Octo regression: a 2-hop paper P that is cited by multiple
// papers that also cite the seed (co-citation bridge) must surface in the
// ranking, even when P shares no references with the seed. The previous
// algorithm zeroed coCite for bridges, which made this structurally
// impossible. With CoCitationApprox the bridge earns a non-zero score
// from seed.citers' refs alone.
func TestBuildSurfacesCoCitationBridge(t *testing.T) {
	// Seed S is cited by C1, C2, C3. All three also cite P (the bridge).
	// P has no direct link to S and no shared refs with S.
	seed := paper("S", nil, []string{"C1", "C2", "C3"})
	seed.CitationCount = 3
	cand := paper("P", nil, nil)
	cand.CitationCount = 3
	s2 := &stubS2{papers: map[string]citation.Paper{
		"S":  seed,
		"C1": paper("C1", []string{"S", "P"}, nil),
		"C2": paper("C2", []string{"S", "P"}, nil),
		"C3": paper("C3", []string{"S", "P"}, nil),
		"P":  cand,
	}}
	b := &Builder{S2: s2, MaxNodes: 10, TwoHopSupport: 2, SimilarityEdgeThreshold: 0.9}

	resp, err := b.Build(context.Background(), "S")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	var pSim float64
	found := false
	for _, n := range resp.Nodes {
		if n.ID == "P" {
			found = true
			pSim = n.Similarity
		}
	}
	if !found {
		t.Fatal("co-citation bridge P missing from graph")
	}
	if pSim <= 0 {
		t.Errorf("bridge P similarity should be > 0 via co-citation; got %v", pSim)
	}
}

// A 2-hop bridge with both high support AND biblio overlap with seed should
// outrank a weakly-coupled direct neighbor. This is the Connected-Papers
// property: a famous depth-2 paper (cited by many of seed's neighbors AND
// sharing references with seed) surfaces higher than a direct ref that
// contributes nothing structural beyond the edge itself.
func TestBuildBridgeOutranksWeakFirstHop(t *testing.T) {
	// S directly refs R1, R2, R3 (so they carry direct_link=0.2) and F (a
	// weak first-hop paper: no refs or cites beyond seed → score stops at
	// the direct bonus). R1/R2/R3 all cite B; B also refs F, giving B a
	// biblio overlap with seed via F.
	s2 := &stubS2{papers: map[string]citation.Paper{
		"S":  paper("S", []string{"R1", "R2", "R3", "F"}, nil),
		"R1": paper("R1", []string{"B"}, nil),
		"R2": paper("R2", []string{"B"}, nil),
		"R3": paper("R3", []string{"B"}, nil),
		"F":  paper("F", nil, nil),
		"B":  paper("B", []string{"F"}, nil),
	}}
	b := &Builder{S2: s2, MaxNodes: 10, TwoHopSupport: 2, SimilarityEdgeThreshold: 0.9}

	resp, err := b.Build(context.Background(), "S")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	sim := map[string]float64{}
	for _, n := range resp.Nodes {
		sim[n.ID] = n.Similarity
	}
	if _, ok := sim["B"]; !ok {
		t.Fatalf("bridge B missing from nodes; got %+v", sim)
	}
	if sim["B"] <= sim["F"] {
		t.Errorf("expected bridge B (support + biblio) to outrank weakly-coupled first-hop F; got B=%v F=%v",
			sim["B"], sim["F"])
	}
}

// recStub records the lookup ids the builder asks for so we can assert it
// (a) calls Recommend on sparse seeds and (b) skips it on dense seeds.
type recStub struct {
	fn    func(ctx context.Context, id string, limit int, fields []string) ([]citation.Paper, error)
	calls []recCall
}
type recCall struct{ id string }

func (r *recStub) Recommend(ctx context.Context, id string, limit int, fields []string) ([]citation.Paper, error) {
	r.calls = append(r.calls, recCall{id: id})
	if r.fn == nil {
		return nil, nil
	}
	return r.fn(ctx, id, limit, fields)
}

// AsyncVLA-shaped: sparse seed (0 refs / 0 cites) with an arxiv DOI. The
// Recommender supplies one neighbour that references the seed; without
// recs the build would yield a single-node graph (the symptom Connected
// Papers avoids by leaning on the same recommendations endpoint).
func TestBuildAugmentsSparseSeedViaRecommender(t *testing.T) {
	s2 := &stubS2{papers: map[string]citation.Paper{
		"S": {
			PaperID:     "S",
			Title:       "Sparse Seed",
			Year:        2025,
			ExternalIDs: citation.ExternalIDs{"DOI": "10.48550/arxiv.0000.0001"},
		},
		"REC1": paper("REC1", []string{"S"}, nil),
	}}
	rec := &recStub{
		fn: func(ctx context.Context, id string, limit int, fields []string) ([]citation.Paper, error) {
			return []citation.Paper{{PaperID: "REC1"}}, nil
		},
	}
	b := &Builder{S2: s2, Recommender: rec, MaxNodes: 10, SimilarityEdgeThreshold: 0.0001}

	resp, err := b.Build(context.Background(), "S")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("recommender should be invoked once for sparse seed, got %d calls", len(rec.calls))
	}
	if want := "DOI:10.48550/arxiv.0000.0001"; rec.calls[0].id != want {
		t.Errorf("recommender lookup id: got %q, want %q", rec.calls[0].id, want)
	}
	have := map[string]bool{}
	for _, n := range resp.Nodes {
		have[n.ID] = true
	}
	if !have["REC1"] {
		t.Errorf("rec was not surfaced in graph; nodes=%v", have)
	}
}

// Dense seeds (≥ threshold refs+cites) have plenty of citation signal on
// their own — paying the extra recs round-trip there is just noise/cost.
func TestBuildSkipsRecommenderOnDenseSeed(t *testing.T) {
	seedRefs := []string{}
	papers := map[string]citation.Paper{}
	for i := 0; i < 12; i++ {
		id := "R" + string(rune('a'+i))
		seedRefs = append(seedRefs, id)
		papers[id] = paper(id, nil, nil)
	}
	papers["S"] = paper("S", seedRefs, nil)
	rec := &recStub{
		fn: func(ctx context.Context, id string, limit int, fields []string) ([]citation.Paper, error) {
			t.Fatalf("recommender must NOT fire for dense seed")
			return nil, nil
		},
	}
	b := &Builder{S2: &stubS2{papers: papers}, Recommender: rec, MaxNodes: 10}
	if _, err := b.Build(context.Background(), "S"); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("recommender called %d times on dense seed, want 0", len(rec.calls))
	}
}

// A seed with neither DOI nor ArXiv id has no way to talk to the recs
// endpoint, so the builder must skip the call without erroring.
func TestBuildSkipsRecommenderWhenSeedHasNoLookupID(t *testing.T) {
	s2 := &stubS2{papers: map[string]citation.Paper{
		"S": {PaperID: "S", Title: "noid"},
	}}
	rec := &recStub{
		fn: func(ctx context.Context, id string, limit int, fields []string) ([]citation.Paper, error) {
			t.Fatalf("recommender must NOT fire when seed lacks DOI/ArXiv")
			return nil, nil
		},
	}
	b := &Builder{S2: s2, Recommender: rec, MaxNodes: 10}
	if _, err := b.Build(context.Background(), "S"); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("recommender called %d times on no-id seed, want 0", len(rec.calls))
	}
}

// Under CITATION_PROVIDER=semanticscholar the seed paper comes back with
// only its 40-char hex paperId — no DOI/ArXiv populated. Without a hex
// fallback the recommender lookup is skipped and the build collapses to
// the plain 4-node graph the recs path was meant to escape.
func TestBuildAugmentsSparseSeedViaHexPaperID(t *testing.T) {
	const hexSeed = "abcdef0123456789abcdef0123456789abcdef01"
	s2 := &stubS2{papers: map[string]citation.Paper{
		hexSeed: {PaperID: hexSeed, Title: "Sparse S2 Seed", Year: 2025},
		"REC1":  paper("REC1", []string{hexSeed}, nil),
	}}
	rec := &recStub{
		fn: func(ctx context.Context, id string, limit int, fields []string) ([]citation.Paper, error) {
			return []citation.Paper{{PaperID: "REC1"}}, nil
		},
	}
	b := &Builder{S2: s2, Recommender: rec, MaxNodes: 10, SimilarityEdgeThreshold: 0.0001}

	resp, err := b.Build(context.Background(), hexSeed)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("want 1 recommender call for hex-only sparse seed, got %d", len(rec.calls))
	}
	if rec.calls[0].id != hexSeed {
		t.Errorf("recommender lookup id: got %q, want raw hex %q", rec.calls[0].id, hexSeed)
	}
	have := map[string]bool{}
	for _, n := range resp.Nodes {
		have[n.ID] = true
	}
	if !have["REC1"] {
		t.Errorf("rec REC1 was not surfaced; nodes=%v", have)
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

// stubCache captures what the Builder reads/writes so the cache-aware
// paths can be asserted without a real Postgres.
type stubCache struct {
	have     map[string]citation.Paper
	upserted []citation.Paper
	links    map[string][2][]string // paperID -> [refs, cites]
}

func (c *stubCache) GetPapersWithLinks(_ context.Context, ids []string) ([]citation.Paper, error) {
	out := make([]citation.Paper, 0, len(ids))
	for _, id := range ids {
		if p, ok := c.have[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *stubCache) UpsertPapers(_ context.Context, papers []citation.Paper) error {
	c.upserted = append(c.upserted, papers...)
	return nil
}

func (c *stubCache) InvalidateGraph(_ context.Context, _ string) error { return nil }

func (c *stubCache) ReplacePaperLinks(_ context.Context, paperID string, refs, cites []string) error {
	if c.links == nil {
		c.links = map[string][2][]string{}
	}
	c.links[paperID] = [2][]string{slices.Clone(refs), slices.Clone(cites)}
	return nil
}

// Cache hit on the seed must short-circuit the S2 GetPaper call.
func TestBuildHitsCacheForSeed(t *testing.T) {
	s2 := &stubS2{papers: map[string]citation.Paper{
		// Intentionally no "S": S2 must NEVER be consulted for the seed.
		"R1": paper("R1", nil, nil),
		"R2": paper("R2", nil, nil),
	}}
	cache := &stubCache{have: map[string]citation.Paper{
		"S": paper("S", []string{"R1", "R2"}, nil),
	}}
	b := &Builder{S2: s2, Cache: cache, MaxNodes: 10}

	resp, err := b.Build(context.Background(), "S")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if resp.Seed.ID != "S" {
		t.Errorf("seed ID = %q, want S", resp.Seed.ID)
	}
}

// After a successful build, selected full-metadata nodes must be pushed
// into the cache (papers table + paper_links) so /api/paper/{id} can
// answer from Postgres without hitting S2.
func TestBuildPersistsFullNodes(t *testing.T) {
	s2 := &stubS2{papers: map[string]citation.Paper{
		"S":  paper("S", []string{"R1", "R2"}, nil),
		"R1": paper("R1", nil, nil),
		"R2": paper("R2", nil, nil),
	}}
	cache := &stubCache{}
	b := &Builder{S2: s2, Cache: cache, MaxNodes: 10}

	if _, err := b.Build(context.Background(), "S"); err != nil {
		t.Fatalf("build: %v", err)
	}

	gotIDs := map[string]bool{}
	for _, p := range cache.upserted {
		gotIDs[p.PaperID] = true
	}
	for _, want := range []string{"S", "R1", "R2"} {
		if !gotIDs[want] {
			t.Errorf("paper %s missing from upsert; got %+v", want, gotIDs)
		}
	}
	if _, ok := cache.links["S"]; !ok {
		t.Error("seed links were not replaced in cache")
	}
}

// Papers flagged CitationsUnknown must not get their link row stamped —
// otherwise a later cache read would hand the Builder an empty Citations
// slice as if the paper had zero citers, corrupting co-citation scoring.
func TestBuildSkipsLinkPersistWhenCitationsUnknown(t *testing.T) {
	seed := paper("S", []string{"R1", "R2"}, nil)
	r1 := paper("R1", []string{"X"}, nil)
	r1.CitationsUnknown = true
	r2 := paper("R2", []string{"X"}, nil)
	s2 := &stubS2{papers: map[string]citation.Paper{
		"S": seed, "R1": r1, "R2": r2,
	}}
	cache := &stubCache{}
	b := &Builder{S2: s2, Cache: cache, MaxNodes: 10}

	if _, err := b.Build(context.Background(), "S"); err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, ok := cache.links["R1"]; ok {
		t.Error("R1 links were persisted despite CitationsUnknown")
	}
	if _, ok := cache.links["R2"]; !ok {
		t.Error("R2 links missing — non-unknown papers must still persist")
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
