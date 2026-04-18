package graph

import (
	"math"
	"slices"
)

// Default weights balance reference overlap, co-citation, and a bonus for
// papers directly linked (cited by seed or citing seed).
const (
	WeightBiblioCoupling = 0.40
	WeightCoCitation     = 0.40
	WeightDirectLink     = 0.20
)

// JaccardLike returns the Salton (cosine) index of two id sets:
//
//	|A ∩ B| / sqrt(|A| * |B|)
//
// which is the shape Connected Papers uses for both bibliographic coupling
// and co-citation. Returns 0 if either side is empty.
func JaccardLike(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(a))
	for _, id := range a {
		set[id] = struct{}{}
	}
	overlap := 0
	for _, id := range b {
		if _, ok := set[id]; ok {
			overlap++
		}
	}
	if overlap == 0 {
		return 0
	}
	return float64(overlap) / math.Sqrt(float64(len(a))*float64(len(b)))
}

// BibliographicCoupling compares what two papers cite.
func BibliographicCoupling(seedRefs, candRefs []string) float64 {
	return JaccardLike(seedRefs, candRefs)
}

// CoCitation compares who cites each paper.
func CoCitation(seedCitedBy, candCitedBy []string) float64 {
	return JaccardLike(seedCitedBy, candCitedBy)
}

// DirectLink returns 1 if the candidate is in the seed's direct citation set.
func DirectLink(seedRefs, seedCitedBy []string, candidateID string) float64 {
	if slices.Contains(seedRefs, candidateID) || slices.Contains(seedCitedBy, candidateID) {
		return 1
	}
	return 0
}

// Score combines the three metrics with default weights.
func Score(biblio, coCite, direct float64) float64 {
	return WeightBiblioCoupling*biblio +
		WeightCoCitation*coCite +
		WeightDirectLink*direct
}
