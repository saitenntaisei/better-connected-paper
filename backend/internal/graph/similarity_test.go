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

func TestDirectLink(t *testing.T) {
	refs := []string{"a", "b"}
	cites := []string{"c"}
	if v := DirectLink(refs, cites, "a"); v != 1 {
		t.Errorf("want 1 for ref member, got %v", v)
	}
	if v := DirectLink(refs, cites, "c"); v != 1 {
		t.Errorf("want 1 for cite member, got %v", v)
	}
	if v := DirectLink(refs, cites, "z"); v != 0 {
		t.Errorf("want 0 for unrelated, got %v", v)
	}
}

func TestScoreIsWeightedSum(t *testing.T) {
	got := Score(1, 1, 1)
	want := WeightBiblioCoupling + WeightCoCitation + WeightDirectLink
	if !almostEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if !almostEqual(want, 1.0) {
		t.Errorf("weights should sum to 1, got %v", want)
	}
}
