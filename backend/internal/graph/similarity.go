package graph

import "math"

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

// CoCitation compares who cites each paper — Salton over the two citer
// id-sets. Used between fully-hydrated candidate pairs when building
// similarity edges.
func CoCitation(seedCitedBy, candCitedBy []string) float64 {
	return JaccardLike(seedCitedBy, candCitedBy)
}

// CoCitationApprox is the seed→candidate co-citation score used during
// ranking. We can't afford to fetch every candidate's citer list (a famous
// bridge paper has thousands of citers, and OpenAlex caps the list at 1000
// anyway), so the numerator is reconstructed from data the Builder already
// has: the seed's citers and their refs (the "refs" side of the first-hop
// fetch). The count
//
//	|{P ∈ seedCiters : cand ∈ P.refs}|
//
// is the exact Salton numerator up to the seed.citers cap. The denominator
// uses total citation counts from the provider, which are cache-invariant
// and don't depend on whether P is a first-hop or 2-hop candidate — so the
// same formula scores direct neighbors and bridge papers on one scale.
func CoCitationApprox(
	seedCiters []string,
	seedCiterRefs map[string]map[string]struct{},
	candID string,
	seedCitationTotal, candCitationTotal int,
) float64 {
	if len(seedCiters) == 0 || seedCitationTotal <= 0 || candCitationTotal <= 0 {
		return 0
	}
	count := 0
	for _, pid := range seedCiters {
		refs, ok := seedCiterRefs[pid]
		if !ok {
			continue
		}
		if _, has := refs[candID]; has {
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return float64(count) / math.Sqrt(float64(seedCitationTotal)*float64(candCitationTotal))
}

// ScoreCP is the Connected Papers-faithful similarity: mean of bibliographic
// coupling and co-citation, both Salton-normalized. A 2-hop bridge cited by
// many of the seed's citers (high coCite) beats a directly-cited paper that
// shares no structure with the seed (biblio=0, coCite=0).
func ScoreCP(biblio, coCite float64) float64 {
	return (biblio + coCite) / 2
}
