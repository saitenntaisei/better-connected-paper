package graph

import (
	"math"
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
		name                       string
		citers                     []string
		seedTotal, candTotal       int
		refsMap                    map[string]map[string]struct{}
		candID                     string
		wantZeroReason             string
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
