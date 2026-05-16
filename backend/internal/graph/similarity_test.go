package graph

import (
	"math"
	"strconv"
	"testing"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestJaccardLikeEmpty(t *testing.T) {
	if v := JaccardLike(nil, []string{"a"}); v != 0 {
		t.Errorf("empty A should return 0, got %v", v)
	}
	if v := JaccardLike([]string{"a"}, nil); v != 0 {
		t.Errorf("empty B should return 0, got %v", v)
	}
}

func TestJaccardLikeOverlap(t *testing.T) {
	a := []string{"x", "y", "z"}
	b := []string{"y", "z", "w"}
	got := JaccardLike(a, b)
	want := 2.0 / math.Sqrt(3.0*3.0) // = 2/3
	if !almostEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestJaccardLikeIdentical(t *testing.T) {
	a := []string{"x", "y", "z"}
	if v := JaccardLike(a, a); !almostEqual(v, 1.0) {
		t.Errorf("identical sets should be 1, got %v", v)
	}
}

func TestCoCitationApproxCountsSeedCitersWithCandInRefs(t *testing.T) {
	seedCiters := []string{"P1", "P2", "P3"}
	// P1 and P3 cite candidate "C"; P2 does not.
	citerRefs := map[string]map[string]struct{}{
		"P1": {"C": {}, "X": {}},
		"P2": {"X": {}},
		"P3": {"C": {}, "Y": {}},
	}
	got := CoCitationApprox(seedCiters, citerRefs, "C", 3, 4)
	want := 2.0 / math.Sqrt(3.0*4.0)
	if !almostEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCoCitationApproxZeroGuards(t *testing.T) {
	refs := map[string]map[string]struct{}{"P1": {"C": {}}}
	cases := []struct {
		name                 string
		citers               []string
		seedTotal, candTotal int
		refsMap              map[string]map[string]struct{}
		candID               string
		wantZeroReason       string
	}{
		{"no citers", nil, 10, 10, refs, "C", "empty seed citers"},
		{"zero seed total", []string{"P1"}, 0, 10, refs, "C", "seed.CitationCount unknown"},
		{"zero cand total", []string{"P1"}, 10, 0, refs, "C", "cand.CitationCount unknown"},
		{"no overlap", []string{"P1"}, 10, 10, refs, "Z", "no citer refs cand"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if v := CoCitationApprox(c.citers, c.refsMap, c.candID, c.seedTotal, c.candTotal); v != 0 {
				t.Errorf("%s: got %v, want 0", c.wantZeroReason, v)
			}
		})
	}
}

func TestScoreCPIsMean(t *testing.T) {
	if v := ScoreCP(0.4, 0.2); !almostEqual(v, 0.3) {
		t.Errorf("got %v, want 0.3", v)
	}
	if v := ScoreCP(0, 0); v != 0 {
		t.Errorf("zero inputs should score 0, got %v", v)
	}
	if v := ScoreCP(1, 1); !almostEqual(v, 1.0) {
		t.Errorf("unit inputs should score 1, got %v", v)
	}
}

// CoCitationApprox is the Salton co-citation score; documented contract
// is [0, 1]. Hybrid supplement can inflate seedCiters past the seed.
// CitationCount field (Octo's S2 inline list returns 1000+ items while
// the RSS-DOI primary reports 61), which would push the unclamped
// raw ratio above 1 — DROID was returning 2.16 against Octo before
// the fix. Verify both the field/observed mismatch handling and the
// final clamp.
func TestCoCitationApproxClampedToOne(t *testing.T) {
	// 50 citers all reference the candidate, but the seed's CitationCount
	// field reports only 10 (stale). Without max() + clamp the ratio
	// would be 50/sqrt(10*1) ≈ 15.8.
	citers := make([]string, 50)
	refs := make(map[string]map[string]struct{}, 50)
	for i := range citers {
		id := "P" + strconv.Itoa(i)
		citers[i] = id
		refs[id] = map[string]struct{}{"C": {}}
	}
	got := CoCitationApprox(citers, refs, "C", 10, 1)
	if got > 1.0 || got < 0 {
		t.Errorf("expected score clamped to [0, 1], got %v", got)
	}
}

func TestYearProximityBonusDecay(t *testing.T) {
	cases := []struct {
		cand, seed int
		want       float64
	}{
		{2024, 2024, 0.12},
		{2023, 2024, 0.096},
		{2025, 2024, 0.096},
		{2019, 2024, 0},
		{0, 2024, 0},
		{2024, 0, 0},
	}
	for _, c := range cases {
		got := yearProximityBonus(c.cand, c.seed)
		if !almostEqual(got, c.want) {
			t.Errorf("yearProximityBonus(cand=%d, seed=%d) = %v, want %v", c.cand, c.seed, got, c.want)
		}
	}
}

func TestCitationCountBonusSaturates(t *testing.T) {
	if got := citationCountBonus(0); got != 0 {
		t.Errorf("cc=0 must contribute 0, got %v", got)
	}
	if got := citationCountBonus(1_000_000); got > 0.081 || got < 0.079 {
		t.Errorf("cc=1M should saturate at the 0.08 ceiling, got %v", got)
	}
	if got := citationCountBonus(100); got <= 0 || got >= 0.08 {
		t.Errorf("cc=100 bonus must sit in (0, 0.08), got %v", got)
	}
}

// cappedRankingBonus must crush the year + cc bonus when a candidate's
// structural score is near zero — that's what surfaced bogus high-cc
// 2-hop bridges (Mizar/Aion-class papers with OpenAlex-side citation
// inflation) in DROID's graph via wrong upstream paper_links. With
// the cap, a struct≈0 candidate inherits a cap≈0 bonus and falls out
// of the top-MaxNodes cut. Genuine candidates with even modest
// structural signal (struct≥0.05) keep the full bonus because the
// 4× multiplier easily covers the 0.20 raw maximum.
func TestCappedRankingBonusCrushesBonusOnLowStructural(t *testing.T) {
	cases := []struct {
		name               string
		structural         float64
		candYear, seedYear int
		candCC             int
		wantMaxBonus       float64
	}{
		// cap = structural × 4, so a struct=0 candidate gets bonus=0
		// regardless of raw year/cc inputs.
		{"zero structural", 0.0, 2024, 2024, 75670, 0.0},
		// near-zero structural: cap = 0.02 << raw ≈ 0.20.
		{"near-zero structural", 0.005, 2024, 2024, 75670, 0.02},
		// modest structural: cap = 0.20 ≥ raw ≈ 0.17, no clamp.
		{"modest structural", 0.05, 2024, 2024, 350, 0.20},
		// strong structural: cap big, raw small (low cc), returned as-is.
		{"strong structural", 0.30, 2024, 2024, 1, 0.13},
	}
	for _, c := range cases {
		got := cappedRankingBonus(c.structural, c.candYear, c.seedYear, c.candCC)
		if got > c.wantMaxBonus+1e-6 {
			t.Errorf("%s: bonus %v exceeds cap %v", c.name, got, c.wantMaxBonus)
		}
	}
}
