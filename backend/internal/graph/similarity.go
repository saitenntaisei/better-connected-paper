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
	// Salton's |A ∩ B| / sqrt(|A| * |B|) requires the denominator to use
	// the *actual* set sizes, but our inputs are best-effort proxies:
	// hybrid supplement can stretch seedCiters past the seed.CitationCount
	// field (S2 inline cap 1000 vs OpenAlex's reported 61 for Octo's RSS
	// DOI), and a candidate's actual citers count can be lower than its
	// observed overlap (count) when citationCount lags behind ingest.
	// Take max(field, observed) on both sides so the score keeps Cauchy-
	// Schwarz's [0, 1] guarantee even on inconsistent data. A trailing
	// clamp catches any residual numerical drift.
	seedCT := seedCitationTotal
	if n := len(seedCiters); n > seedCT {
		seedCT = n
	}
	candCT := candCitationTotal
	if count > candCT {
		candCT = count
	}
	result := float64(count) / math.Sqrt(float64(seedCT)*float64(candCT))
	if result > 1.0 {
		result = 1.0
	}
	return result
}

// ScoreCP is the Connected Papers-faithful similarity: mean of bibliographic
// coupling and co-citation, both Salton-normalized. A 2-hop bridge cited by
// many of the seed's citers (high coCite) beats a directly-cited paper that
// shares no structure with the seed (biblio=0, coCite=0).
func ScoreCP(biblio, coCite float64) float64 {
	return (biblio + coCite) / 2
}

// cappedRankingBonus returns rankingBonus clamped to
// structuralBonusMultiplier × structural, so a candidate with zero or
// near-zero biblio+coCite score can't be propped up to top-MaxNodes
// by bonus signals alone. Bogus paper_links edges from upstream data
// (e.g. OpenAlex citation-count inflation on Mizar / Aion-class
// works that wrongly appear as 2-hop bridges) trip this guard:
// their structural score stays at the floor, so the bonus is
// proportionally crushed and they drop out of the cut.
//
// structuralBonusMultiplier = 4 keeps the cap from constraining
// genuine candidates: a struct≈0.04 direct ref like Diffusion Policy
// against Octo allows up to 0.16 bonus, easily covering the 0.20
// max from yearProximity + citationCount; a struct≈0.20 well-coupled
// peer can soak the full bonus without being capped. Only candidates
// with struct < ~0.05 see the cap bite.
func cappedRankingBonus(structural float64, candYear, seedYear, candCC int) float64 {
	const structuralBonusMultiplier = 4.0
	raw := rankingBonus(candYear, seedYear, candCC)
	cap := structural * structuralBonusMultiplier
	if raw > cap {
		return cap
	}
	return raw
}

// rankingBonus adds two orthogonal Connected-Papers-like signals on top
// of the structural Salton score so the top-MaxNodes cut matches the
// "few dozen most-related" cluster a human would expect.
//
// Component 1 — year proximity (weight 0.12). Connected Papers' Octo
// graph stays inside 2022–2025; the seed-year signal is what keeps the
// cluster from collapsing onto deep CV foundations whose biblio
// coupling against the seed's own ref list scores nontrivially.
// Linear decay over 5 years: gap=0 → full bonus, gap≥5 → 0.
//
// Component 2 — log-citation count (weight 0.08, saturating). Without
// this, foundational direct refs like Diffusion Policy (struct≈0.04
// against Octo) get pushed out of top-MaxNodes by same-year preprints
// whose modern reference set overlaps Octo's better but topically
// matters less. The log curve saturates around cc=10000 so an
// arbitrarily-cited seminal paper doesn't dominate; the year decay
// keeps ImageNet 2012 (cc 75k+) from sneaking back in regardless.
//
// Tuning: combined max bonus 0.20, applied to scores typically in
// [0, 0.5]. PaLM-E (2023, cc=350) lifts +0.17 and lands top-15 on Octo;
// VoxPoser (2023, cc=87) lifts +0.13 and stays in top-30; ImageNet
// (2012, cc=75k) lifts only +0.08 from the cc side, structural ~0.01,
// stays out. Either bonus contributes 0 with missing metadata so the
// structural signal remains authoritative.
func rankingBonus(candYear, seedYear, candCC int) float64 {
	return yearProximityBonus(candYear, seedYear) + citationCountBonus(candCC)
}

func yearProximityBonus(candYear, seedYear int) float64 {
	if seedYear <= 0 || candYear <= 0 {
		return 0
	}
	diff := candYear - seedYear
	if diff < 0 {
		diff = -diff
	}
	const (
		yearWeight = 0.12
		maxYearGap = 5
	)
	if diff >= maxYearGap {
		return 0
	}
	return yearWeight * (1.0 - float64(diff)/float64(maxYearGap))
}

func citationCountBonus(cc int) float64 {
	if cc <= 0 {
		return 0
	}
	const (
		ccWeight = 0.08
		ccCap    = 10000.0
	)
	denom := math.Log10(ccCap + 1)
	v := math.Log10(float64(cc+1)) / denom
	if v > 1.0 {
		v = 1.0
	}
	return ccWeight * v
}
