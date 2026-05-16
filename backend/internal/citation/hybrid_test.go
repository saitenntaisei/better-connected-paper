package citation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type stubProvider struct {
	search     func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error)
	getPaper   func(ctx context.Context, id string, fields []string) (*Paper, error)
	getBatch   func(ctx context.Context, ids []string, fields []string) ([]Paper, error)
	recommend  func(ctx context.Context, id string, limit int, fields []string) ([]Paper, error)
	calls      []stubCall
	batchCalls []stubBatchCall
	recCalls   []stubCall
}

type stubCall struct {
	id     string
	fields []string
}

type stubBatchCall struct {
	ids    []string
	fields []string
}

func (s *stubProvider) Search(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
	if s.search == nil {
		return &SearchResponse{}, nil
	}
	return s.search(ctx, q, limit, fields)
}

func (s *stubProvider) GetPaper(ctx context.Context, id string, fields []string) (*Paper, error) {
	s.calls = append(s.calls, stubCall{id: id, fields: fields})
	return s.getPaper(ctx, id, fields)
}

func (s *stubProvider) GetPaperBatch(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
	s.batchCalls = append(s.batchCalls, stubBatchCall{ids: ids, fields: fields})
	return s.getBatch(ctx, ids, fields)
}

// Recommend lets stubProvider double as a Recommender for tests that wire
// it as ResolvingTertiary.Inner. Methods without a wired func surface a
// nil-deref so the test fails loudly if the code under test calls Recommend
// when it shouldn't.
func (s *stubProvider) Recommend(ctx context.Context, id string, limit int, fields []string) ([]Paper, error) {
	s.recCalls = append(s.recCalls, stubCall{id: id, fields: fields})
	return s.recommend(ctx, id, limit, fields)
}

func TestHybridSearchAlwaysPrimary(t *testing.T) {
	primary := &stubProvider{search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
		return &SearchResponse{Total: 1, Data: []Paper{{PaperID: "W1", Title: "ok"}}}, nil
	}}
	secondary := &stubProvider{search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
		t.Fatalf("secondary search must not be called")
		return nil, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary}
	resp, err := h.Search(context.Background(), "q", 5, nil)
	if err != nil || resp == nil || resp.Total != 1 {
		t.Fatalf("unexpected search result: resp=%+v err=%v", resp, err)
	}
}

func TestHybridGetPaperPrimarySufficient(t *testing.T) {
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:    "W1",
			References: []Paper{{PaperID: "W11"}, {PaperID: "W12"}},
		}, nil
	}}
	secondary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		t.Fatalf("secondary must not be called when primary returned refs")
		return nil, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary}
	p, err := h.GetPaper(context.Background(), "W1", []string{"paperId", "references.paperId"})
	if err != nil || p == nil || len(p.References) != 2 {
		t.Fatalf("unexpected: p=%+v err=%v", p, err)
	}
}

func TestHybridSupplementsEmptyRefs(t *testing.T) {
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:        "W1",
			Title:          "Conference paper",
			ReferenceCount: 50,
			// Non-arxiv DOI keeps OpenCitations in the supplement chain.
			// Hybrid skips secondary for arxiv preprints because the
			// OpenCitations corpus doesn't index them, so this test
			// pins the secondary-supplements-refs path on a paper that
			// genuinely benefits from secondary coverage.
			ExternalIDs: ExternalIDs{"DOI": "10.1109/cvpr.2024.99999"},
		}, nil
	}}
	var secondaryID string
	secondary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		secondaryID = id
		return &Paper{
			PaperID:    "abc123",
			References: []Paper{{PaperID: "sha_ref_1"}, {PaperID: "sha_ref_2"}, {PaperID: "sha_ref_3"}},
		}, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary}
	p, err := h.GetPaper(context.Background(), "W1", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p.PaperID != "W1" {
		t.Fatalf("PaperID must stay primary W1, got %q", p.PaperID)
	}
	if p.Title != "Conference paper" {
		t.Fatalf("primary metadata must survive merge, got title %q", p.Title)
	}
	if len(p.References) != 3 {
		t.Fatalf("refs must come from secondary, got %d", len(p.References))
	}
	if secondaryID != "DOI:10.1109/cvpr.2024.99999" {
		t.Fatalf("secondary lookup id wrong: %q", secondaryID)
	}
}

// OpenCitations never has arxiv preprint data, so the seed-fetch path
// must skip it and go straight to tertiary — saves ~1-2 s per sparse
// arxiv seed by not paying for a guaranteed-empty round-trip.
func TestHybridSkipsSecondaryForArxivPreprints(t *testing.T) {
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:     "W7106158755",
			Title:       "AsyncVLA",
			ExternalIDs: ExternalIDs{"DOI": "10.48550/arxiv.2511.14148"},
		}, nil
	}}
	secondary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		t.Fatalf("secondary must not fire for an arxiv preprint seed")
		return nil, nil
	}}
	var tertiaryID string
	tertiary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		tertiaryID = id
		return &Paper{References: []Paper{{PaperID: "TREF1"}}}, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary, Tertiary: tertiary}
	p, err := h.GetPaper(context.Background(), "W7106158755", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if tertiaryID != "DOI:10.48550/arxiv.2511.14148" {
		t.Errorf("tertiary lookup id: got %q, want DOI:10.48550/arxiv.2511.14148", tertiaryID)
	}
	if len(p.References) != 1 {
		t.Errorf("want 1 ref from tertiary supplement, got %d", len(p.References))
	}
}

func TestHybridSupplementsCitesUnknown(t *testing.T) {
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:          "W2",
			References:       []Paper{{PaperID: "W21"}}, // refs OK
			CitationsUnknown: true,
			ExternalIDs:      ExternalIDs{"DOI": "10.1/foo"},
		}, nil
	}}
	secondary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:   "sha_s",
			Citations: []Paper{{PaperID: "sha_c1"}, {PaperID: "sha_c2"}},
		}, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary}
	p, err := h.GetPaper(context.Background(), "W2", []string{"paperId", "references.paperId", "citations.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if p.CitationsUnknown {
		t.Fatal("CitationsUnknown should be cleared when secondary supplied cites")
	}
	if len(p.Citations) != 2 {
		t.Fatalf("cites not merged, got %d", len(p.Citations))
	}
	if len(p.References) != 1 || p.References[0].PaperID != "W21" {
		t.Fatalf("primary refs must be preserved, got %+v", p.References)
	}
}

func TestHybridDoesNotShrinkPrimaryCites(t *testing.T) {
	// OpenCitations often has less coverage than OpenAlex on a given paper;
	// if the merge blindly replaced primary cites with the shorter secondary
	// list we'd regress the cite graph. The merge must keep the longer list.
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		cites := make([]Paper, 60)
		for i := range cites {
			cites[i] = Paper{PaperID: "W_citer_" + string(rune('A'+i%26))}
		}
		return &Paper{
			PaperID:          "W1",
			ReferenceCount:   99,    // triggers refsGap
			Citations:        cites, // primary already has 60 cites
			CitationsUnknown: false,
			ExternalIDs:      ExternalIDs{"DOI": "10.1/x"},
		}, nil
	}}
	secondary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		// secondary returns only 19 cites — must NOT overwrite primary's 60
		short := make([]Paper, 19)
		for i := range short {
			short[i] = Paper{PaperID: "W_short_" + string(rune('a'+i))}
		}
		return &Paper{Citations: short}, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary}
	p, err := h.GetPaper(context.Background(), "W1", []string{"paperId", "references.paperId", "citations.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Citations) != 60 {
		t.Fatalf("expected 60 cites preserved from primary, got %d", len(p.Citations))
	}
}

func TestHybridNoSupplementWhenNoLookupID(t *testing.T) {
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:        "W3",
			ReferenceCount: 10,
			ExternalIDs:    ExternalIDs{}, // no DOI, no arxiv
		}, nil
	}}
	secondary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		t.Fatalf("secondary must not be called without a lookup id")
		return nil, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary}
	p, err := h.GetPaper(context.Background(), "W3", []string{"paperId", "references.paperId"})
	if err != nil || p == nil || p.PaperID != "W3" {
		t.Fatalf("primary must be returned as-is: p=%+v err=%v", p, err)
	}
}

func TestHybridSecondaryFailureReturnsPrimary(t *testing.T) {
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:        "W4",
			ReferenceCount: 5,
			ExternalIDs:    ExternalIDs{"DOI": "10.2/bar"},
		}, nil
	}}
	secondary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return nil, errors.New("boom")
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary}
	p, err := h.GetPaper(context.Background(), "W4", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatalf("secondary error must not surface: %v", err)
	}
	if p.PaperID != "W4" {
		t.Fatalf("expected primary W4, got %q", p.PaperID)
	}
}

func TestHybridRoutesSecondaryIDDirect(t *testing.T) {
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		t.Fatalf("primary must not be called for secondary-shape id")
		return nil, nil
	}}
	var seenID string
	secondary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		seenID = id
		return &Paper{PaperID: id}, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary}
	// 40-char hex = S2 paperId shape
	s2id := "0123456789abcdef0123456789abcdef01234567"
	p, err := h.GetPaper(context.Background(), s2id, []string{"paperId"})
	if err != nil || p.PaperID != s2id {
		t.Fatalf("unexpected: p=%+v err=%v", p, err)
	}
	if seenID != s2id {
		t.Fatalf("secondary got wrong id: %q", seenID)
	}
}

func TestHybridBatchSplitsByShape(t *testing.T) {
	primary := &stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
		out := make([]Paper, 0, len(ids))
		for _, id := range ids {
			out = append(out, Paper{PaperID: id})
		}
		return out, nil
	}}
	secondary := &stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
		out := make([]Paper, 0, len(ids))
		for _, id := range ids {
			out = append(out, Paper{PaperID: id})
		}
		return out, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary}
	s2id := "0123456789abcdef0123456789abcdef01234567"
	papers, err := h.GetPaperBatch(context.Background(), []string{"W1", s2id, "W2"}, []string{"paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if len(papers) != 3 {
		t.Fatalf("expected 3, got %d", len(papers))
	}
	if len(primary.batchCalls) != 1 || len(primary.batchCalls[0].ids) != 2 {
		t.Fatalf("primary should see 2 W-IDs, got %+v", primary.batchCalls)
	}
	if len(secondary.batchCalls) != 1 || len(secondary.batchCalls[0].ids) != 1 {
		t.Fatalf("secondary should see 1 S2 ID, got %+v", secondary.batchCalls)
	}
}

func TestHybridBatchNoSecondaryWhenConfigured(t *testing.T) {
	primary := &stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
		out := make([]Paper, 0, len(ids))
		for _, id := range ids {
			out = append(out, Paper{PaperID: id})
		}
		return out, nil
	}}

	h := &HybridClient{Primary: primary}
	s2id := "0123456789abcdef0123456789abcdef01234567"
	_, err := h.GetPaperBatch(context.Background(), []string{s2id}, []string{"paperId"})
	if err == nil {
		t.Fatal("expected error when S2 IDs requested but secondary nil")
	}
}

func TestIsSecondaryID(t *testing.T) {
	cases := map[string]bool{
		"W4402353985": false,
		"0123456789abcdef0123456789abcdef01234567": true,
		"0123456789ABCDEF0123456789abcdef01234567": true,
		"10.48550/arXiv.2405.12213":                false,
		"DOI:10.1/foo":                             false,
		"abc":                                      false,
		"":                                         false,
		"0123456789abcdef0123456789abcdef0123456g": false, // not hex
	}
	for id, want := range cases {
		if got := isSecondaryID(id); got != want {
			t.Errorf("isSecondaryID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestHybridTertiaryFillsGapWhenSecondaryEmpty(t *testing.T) {
	// Octo-ish scenario: primary (OpenAlex) has no refs for the RSS DOI,
	// secondary (OpenCitations) also returns nothing, and only tertiary
	// (S2 via title fallback) finds the refs. The chain must keep walking
	// past an empty secondary instead of giving up.
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:        "W1",
			Title:          "Octo",
			ReferenceCount: 99,
			ExternalIDs:    ExternalIDs{"DOI": "10.15607/rss.2024.xx.090"},
		}, nil
	}}
	secondary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{}, nil // OpenCitations returns nothing for this DOI
		},
		search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
			return &SearchResponse{}, nil // OpenCitations has no search either
		},
	}
	var tertiaryDirectID string
	tertiary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			tertiaryDirectID = id
			return &Paper{
				References: []Paper{{PaperID: "W_ref_1"}, {PaperID: "W_ref_2"}, {PaperID: "W_ref_3"}},
			}, nil
		},
		search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
			t.Fatalf("tertiary search must not be called when direct lookup succeeded")
			return nil, nil
		},
	}

	h := &HybridClient{Primary: primary, Secondary: secondary, Tertiary: tertiary}
	p, err := h.GetPaper(context.Background(), "W1", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p.PaperID != "W1" {
		t.Fatalf("PaperID must stay primary W1, got %q", p.PaperID)
	}
	if len(p.References) != 3 {
		t.Fatalf("tertiary refs not merged, got %d", len(p.References))
	}
	if tertiaryDirectID != "DOI:10.15607/rss.2024.xx.090" {
		t.Fatalf("tertiary lookup id wrong: %q", tertiaryDirectID)
	}
	if len(secondary.calls) == 0 {
		t.Fatalf("secondary must be tried before tertiary")
	}
}

func TestHybridSupplementPicksBetterOfParallelLayers(t *testing.T) {
	// With the parallel-supplement refactor the chain no longer
	// short-circuits when secondary narrows — both layers fire and the
	// broader refs/cites list wins. Verify (1) both providers were
	// called, and (2) tertiary's longer list survives the merge even
	// though secondary also returned data.
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:        "W1",
			ReferenceCount: 20,
			ExternalIDs:    ExternalIDs{"DOI": "10.1/x"},
		}, nil
	}}
	secondary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{References: []Paper{{PaperID: "W_a"}, {PaperID: "W_b"}}}, nil
	}}
	tertiaryCalls := 0
	tertiary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		tertiaryCalls++
		return &Paper{References: []Paper{{PaperID: "W_c"}, {PaperID: "W_d"}, {PaperID: "W_e"}}}, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary, Tertiary: tertiary}
	p, err := h.GetPaper(context.Background(), "W1", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if tertiaryCalls == 0 {
		t.Fatalf("tertiary must run in parallel even when secondary returns data")
	}
	if len(p.References) != 3 {
		t.Fatalf("tertiary's longer ref list must win the merge, got %d", len(p.References))
	}
}

func TestHybridTertiaryRunsAfterSecondaryHardError(t *testing.T) {
	// A hard error on secondary (not 404, not rate-limited) used to stop the
	// supplement path entirely. With the chain refactor, tertiary still gets
	// its turn — one sick provider shouldn't veto a healthy one.
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:        "W1",
			ReferenceCount: 99,
			ExternalIDs:    ExternalIDs{"DOI": "10.1/x"},
		}, nil
	}}
	secondary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return nil, errors.New("upstream 503")
		},
		search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
			return &SearchResponse{}, nil
		},
	}
	tertiary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{References: []Paper{{PaperID: "W_tt"}}}, nil
	}}

	h := &HybridClient{Primary: primary, Secondary: secondary, Tertiary: tertiary}
	p, err := h.GetPaper(context.Background(), "W1", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.References) != 1 || p.References[0].PaperID != "W_tt" {
		t.Fatalf("expected tertiary refs after secondary hard error, got %+v", p.References)
	}
}

func TestHybridTertiaryRunsWhenSecondaryAddsNothing(t *testing.T) {
	// Real-world case seen on Octo: OpenCitations returned a shorter cites
	// subset than OpenAlex already had, which used to count as "success"
	// and silenced the tertiary that held the actual refs. The cascade
	// must keep walking whenever the merge didn't grow refs or cites.
	primaryCites := make([]Paper, 60)
	for i := range primaryCites {
		primaryCites[i] = Paper{PaperID: "W_p" + string(rune('A'+i%26))}
	}
	primary := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{
			PaperID:        "W1",
			Title:          "Octo",
			ReferenceCount: 99,
			Citations:      primaryCites,
			ExternalIDs:    ExternalIDs{"DOI": "10.1/octo"},
		}, nil
	}}
	secondary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			// Fewer cites than primary and zero refs — merge will be a no-op.
			short := make([]Paper, 15)
			for i := range short {
				short[i] = Paper{PaperID: "W_s" + string(rune('a'+i))}
			}
			return &Paper{Citations: short}, nil
		},
		search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
			return &SearchResponse{}, nil
		},
	}
	tertiaryHit := false
	tertiary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			tertiaryHit = true
			return &Paper{References: []Paper{{PaperID: "W_t1"}, {PaperID: "W_t2"}, {PaperID: "W_t3"}}}, nil
		},
	}

	h := &HybridClient{Primary: primary, Secondary: secondary, Tertiary: tertiary}
	p, err := h.GetPaper(context.Background(), "W1", []string{"paperId", "references.paperId", "citations.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if !tertiaryHit {
		t.Fatal("tertiary must run when secondary's merge added nothing new")
	}
	if len(p.References) != 3 {
		t.Fatalf("expected tertiary refs to fill the gap, got %d", len(p.References))
	}
	if len(p.Citations) != 60 {
		t.Fatalf("primary cites must survive a no-progress secondary merge, got %d", len(p.Citations))
	}
}

func TestHybridTertiaryFallsBackToArxivSibling(t *testing.T) {
	// Octo case: primary DOI is the RSS 2024 conference DOI S2 doesn't index,
	// but OpenAlex also indexes the same paper as a sibling arxiv work under
	// 10.48550/arxiv.XXXX. The cascade must discover that sibling via a title
	// search and retry the tertiary direct lookup with the arxiv DOI.
	primary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{
				PaperID:        "W_rss",
				Title:          "Octo: An Open-Source Generalist Robot Policy",
				Year:           2024,
				ReferenceCount: 99,
				ExternalIDs:    ExternalIDs{"DOI": "10.15607/rss.2024.xx.090"},
			}, nil
		},
		search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
			return &SearchResponse{Data: []Paper{
				{PaperID: "W_rss", Title: "Octo: An Open-Source Generalist Robot Policy", Year: 2024, ExternalIDs: ExternalIDs{"DOI": "10.15607/rss.2024.xx.090"}},
				{PaperID: "W_arxiv", Title: "Octo: An Open-Source Generalist Robot Policy", Year: 2024, ExternalIDs: ExternalIDs{"DOI": "10.48550/arxiv.2405.12213"}},
			}}, nil
		},
	}
	secondary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			// OpenCitations has nothing useful — empty refs.
			return &Paper{}, nil
		},
	}
	var tertiaryLookups []string
	tertiary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			tertiaryLookups = append(tertiaryLookups, id)
			if id == "DOI:10.15607/rss.2024.xx.090" {
				return nil, ErrNotFound
			}
			if id == "DOI:10.48550/arxiv.2405.12213" {
				return &Paper{References: []Paper{{PaperID: "W_r1"}, {PaperID: "W_r2"}}}, nil
			}
			return nil, ErrNotFound
		},
	}

	h := &HybridClient{Primary: primary, Secondary: secondary, Tertiary: tertiary}
	p, err := h.GetPaper(context.Background(), "W_rss", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.References) != 2 {
		t.Fatalf("expected tertiary arxiv-sibling refs merged, got %d", len(p.References))
	}
	if len(tertiaryLookups) < 2 || tertiaryLookups[len(tertiaryLookups)-1] != "DOI:10.48550/arxiv.2405.12213" {
		t.Fatalf("expected arxiv sibling lookup after primary DOI miss, got %v", tertiaryLookups)
	}
}

func TestHybridDeferSkipsSiblingAndTitleFallback(t *testing.T) {
	// Deferred-ar5iv mode: trySupplement must stop after the direct DOI
	// lookup. The arxiv-sibling probe (which costs a primary Search) and
	// the byTitle fallback (two sequential tertiary calls) are the slow
	// paths the optimization removes from the foreground.
	primary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{
				PaperID:     "W_rss",
				Title:       "Octo: An Open-Source Generalist Robot Policy",
				Year:        2024,
				ExternalIDs: ExternalIDs{"DOI": "10.15607/rss.2024.xx.090"},
			}, nil
		},
		search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
			t.Fatalf("primary search must not fire when defer flag is set (arxivSiblingLookup skipped)")
			return nil, nil
		},
	}
	var tertiaryLookups []string
	tertiary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			tertiaryLookups = append(tertiaryLookups, id)
			// Direct lookup misses; in defer mode the chain returns nil
			// here without walking sibling/title.
			return nil, ErrNotFound
		},
		search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
			t.Fatalf("tertiary search must not fire when defer flag is set (byTitle skipped)")
			return nil, nil
		},
	}

	h := &HybridClient{Primary: primary, Tertiary: tertiary}
	ctx := WithSkipAr5iv(context.Background(), true)
	p, err := h.GetPaper(ctx, "W_rss", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if p.PaperID != "W_rss" {
		t.Fatalf("expected primary paper returned untouched, got %+v", p)
	}
	if len(p.References) != 0 {
		t.Fatalf("expected zero merged refs (defer skips fallbacks), got %d", len(p.References))
	}
	if len(tertiaryLookups) != 1 || tertiaryLookups[0] != "DOI:10.15607/rss.2024.xx.090" {
		t.Fatalf("expected single direct-DOI tertiary call, got %v", tertiaryLookups)
	}
}

func TestHybridDeferOffStillWalksFallbacks(t *testing.T) {
	// Mirror of the defer-skip test with the flag absent: trySupplement
	// must still walk sibling+byTitle. Pins the contract so a future
	// edit that flips the gate doesn't silently regress the non-defer
	// path used by the background ar5iv re-run.
	primary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{
				PaperID:     "W_rss",
				Title:       "Octo: An Open-Source Generalist Robot Policy",
				Year:        2024,
				ExternalIDs: ExternalIDs{"DOI": "10.15607/rss.2024.xx.090"},
			}, nil
		},
		search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
			return &SearchResponse{Data: []Paper{
				{PaperID: "W_arxiv", Title: "Octo: An Open-Source Generalist Robot Policy", Year: 2024, ExternalIDs: ExternalIDs{"DOI": "10.48550/arxiv.2405.12213"}},
			}}, nil
		},
	}
	var tertiaryLookups []string
	tertiary := &stubProvider{
		getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			tertiaryLookups = append(tertiaryLookups, id)
			if id == "DOI:10.48550/arxiv.2405.12213" {
				return &Paper{References: []Paper{{PaperID: "W_r1"}}}, nil
			}
			return nil, ErrNotFound
		},
	}

	h := &HybridClient{Primary: primary, Tertiary: tertiary}
	p, err := h.GetPaper(context.Background(), "W_rss", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.References) != 1 {
		t.Fatalf("expected sibling fallback to populate refs, got %d", len(p.References))
	}
	if len(tertiaryLookups) < 2 {
		t.Fatalf("expected sibling lookup to extend tertiary calls, got %v", tertiaryLookups)
	}
}

func TestArxivSiblingLookupSkippedWhenPrimaryIsArxiv(t *testing.T) {
	// When the primary DOI is already the arxiv form there's nothing new for
	// the sibling probe to find — the extra search is skipped to avoid
	// redundant OpenAlex traffic on every seed.
	primary := &stubProvider{
		search: func(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
			t.Fatalf("primary search must not fire for arxiv-DOI primary")
			return nil, nil
		},
	}
	h := &HybridClient{Primary: primary}
	got := h.arxivSiblingLookupID(context.Background(), &Paper{
		Title:       "X",
		Year:        2024,
		ExternalIDs: ExternalIDs{"DOI": "10.48550/arXiv.1234.5678"},
	})
	if got != "" {
		t.Fatalf("arxiv-DOI primary should yield empty sibling id, got %q", got)
	}
}

func TestResolvingTertiaryTranslatesRefs(t *testing.T) {
	// Inner (S2-shaped) returns refs carrying S2 hex paperIds + DOIs on
	// externalIds. ResolvingTertiary must resolve those DOIs into primary
	// W-IDs via the injected resolver and replace the refs list.
	inner := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		// S2 style: hex paperId on the outer paper, and nested refs with DOIs.
		return &Paper{
			PaperID: "abcdef0123456789abcdef0123456789abcdef01",
			Title:   "Octo",
			References: []Paper{
				{PaperID: "hex_a", ExternalIDs: ExternalIDs{"DOI": "10.1/a"}},
				{PaperID: "hex_b", ExternalIDs: ExternalIDs{"DOI": "10.1/B"}}, // uppercase -> normalized lower
				{PaperID: "hex_c", ExternalIDs: ExternalIDs{"DOI": "10.1/a"}}, // dupe -> deduped
				{PaperID: "hex_d"}, // no DOI -> dropped
			},
			Citations: []Paper{
				{PaperID: "hex_e", ExternalIDs: ExternalIDs{"DOI": "10.2/e"}},
			},
		}, nil
	}}

	var resolverCalls [][]string
	resolver := func(ctx context.Context, dois []string) ([]Paper, error) {
		resolverCalls = append(resolverCalls, append([]string(nil), dois...))
		out := make([]Paper, 0, len(dois))
		for _, d := range dois {
			out = append(out, Paper{PaperID: "W_" + d})
		}
		return out, nil
	}

	r := &ResolvingTertiary{Inner: inner, Resolver: resolver}
	p, err := r.GetPaper(context.Background(), "abcdef0123456789abcdef0123456789abcdef01", []string{"paperId", "references.paperId", "citations.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if p.PaperID != "" {
		t.Fatalf("tertiary adapter must clear PaperID so merge doesn't set MergedFromID, got %q", p.PaperID)
	}
	if len(p.References) != 2 {
		t.Fatalf("expected 2 resolved refs (dedup + drop no-DOI), got %d: %+v", len(p.References), p.References)
	}
	if len(p.Citations) != 1 || p.Citations[0].PaperID != "W_10.2/e" {
		t.Fatalf("expected 1 resolved cite, got %+v", p.Citations)
	}
	if len(inner.calls) != 1 {
		t.Fatalf("expected 1 inner call, got %d", len(inner.calls))
	}
	gotFields := inner.calls[0].fields
	if !slices.Contains(gotFields, "references.externalIds") {
		t.Fatalf("adapter must inject references.externalIds, got %v", gotFields)
	}
	if !slices.Contains(gotFields, "citations.externalIds") {
		t.Fatalf("adapter must inject citations.externalIds, got %v", gotFields)
	}
	if len(resolverCalls) != 2 {
		t.Fatalf("expected 2 resolver calls (refs + cites), got %d", len(resolverCalls))
	}
}

func TestResolvingTertiaryDoesNotEnrichWhenCallerDidNotAsk(t *testing.T) {
	inner := &stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
		return &Paper{Title: "x"}, nil
	}}
	r := &ResolvingTertiary{Inner: inner}
	if _, err := r.GetPaper(context.Background(), "id", []string{"paperId", "title"}); err != nil {
		t.Fatal(err)
	}
	gotFields := inner.calls[0].fields
	for _, f := range gotFields {
		if strings.HasPrefix(f, "references") || strings.HasPrefix(f, "citations") {
			t.Fatalf("enrichment must not add ref/cite fields when caller didn't ask, got %v", gotFields)
		}
	}
}

// stubRefLister is a stubProvider that also satisfies referencePager so
// tests can wire ResolvingTertiary's single-page /references fallback.
// Concurrent callers (the batch supplement now spawns one goroutine per
// paper) share refCalls — the mutex keeps the slice append race-free.
type stubRefLister struct {
	stubProvider
	refs     func(ctx context.Context, id string, fields []string) ([]Paper, error)
	mu       sync.Mutex
	refCalls []refCall
}

type refCall struct {
	id string
}

func (s *stubRefLister) GetReferencesSinglePage(ctx context.Context, id string, fields []string) ([]Paper, error) {
	s.mu.Lock()
	s.refCalls = append(s.refCalls, refCall{id: id})
	s.mu.Unlock()
	if s.refs == nil {
		return nil, nil
	}
	return s.refs(ctx, id, fields)
}

func TestResolvingTertiaryFallsBackToPaginatedRefsWhenInlineEmpty(t *testing.T) {
	// AsyncVLA-style: S2's /paper response has zero inline refs (the field
	// isn't populated for many recent arxiv preprints), but the paginated
	// /paper/{id}/references endpoint does return refs. The tertiary must
	// notice the empty inline result and fall back to the paginated call,
	// then translate the cited papers into primary-space W-IDs.
	inner := &stubRefLister{
		stubProvider: stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{
				PaperID:    "S2HEX",
				References: nil, // inline empty
			}, nil
		}},
		refs: func(ctx context.Context, id string, fields []string) ([]Paper, error) {
			return []Paper{
				{ExternalIDs: ExternalIDs{"DOI": "10.1/a"}},
				{ExternalIDs: ExternalIDs{"ArXiv": "2511.99001"}},
			}, nil
		},
	}
	resolver := func(ctx context.Context, dois []string) ([]Paper, error) {
		out := make([]Paper, 0, len(dois))
		for _, d := range dois {
			out = append(out, Paper{PaperID: "W_" + d})
		}
		return out, nil
	}
	r := &ResolvingTertiary{Inner: inner, Resolver: resolver}

	p, err := r.GetPaper(context.Background(), "ARXIV:2511.14148", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one HTTP request: the single-page interface guarantees
	// listPapers' auto-pagination can't double-spend the per-build budget.
	if len(inner.refCalls) != 1 {
		t.Fatalf("paginated /references must be called once when inline empty, got %d", len(inner.refCalls))
	}
	if len(p.References) != 2 {
		t.Fatalf("want 2 translated refs (DOI + arxiv-synthesised), got %d: %+v", len(p.References), p.References)
	}
}

func TestResolvingTertiarySkipsPaginatedRefsWhenInlinePresent(t *testing.T) {
	// Inline already populated → no need to pay for an extra paginated call.
	inner := &stubRefLister{
		stubProvider: stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{
				PaperID:    "S2HEX",
				References: []Paper{{ExternalIDs: ExternalIDs{"DOI": "10.1/a"}}},
			}, nil
		}},
		refs: func(ctx context.Context, id string, fields []string) ([]Paper, error) {
			t.Fatalf("paginated /references must not fire when inline refs already populated")
			return nil, nil
		},
	}
	resolver := func(ctx context.Context, dois []string) ([]Paper, error) {
		return []Paper{{PaperID: "W_x"}}, nil
	}
	r := &ResolvingTertiary{Inner: inner, Resolver: resolver}
	if _, err := r.GetPaper(context.Background(), "ARXIV:Y", []string{"paperId", "references.paperId"}); err != nil {
		t.Fatal(err)
	}
	if len(inner.refCalls) != 0 {
		t.Fatalf("expected 0 paginated calls, got %d", len(inner.refCalls))
	}
}

// A per-build budget shared across multiple GetPaperBatch calls is what
// keeps a chunked first-hop (BatchSize 100, MaxFirstHop 300) from firing
// 6 × per-batch budgets in series and blowing the Vercel function cap.
// Two consecutive batches drawing from the same WithRefsBackfillBudget
// ctx must split the budget, not get a fresh allowance each.
func TestResolvingTertiaryBatchCapsPaginatedRefsByBuildBudget(t *testing.T) {
	const budget = 4
	makeBatch := func(prefix string, n int) ([]string, []Paper) {
		ids := make([]string, n)
		ps := make([]Paper, n)
		for i := 0; i < n; i++ {
			ids[i] = fmt.Sprintf("%s%d", prefix, i)
			ps[i] = Paper{PaperID: ids[i]}
		}
		return ids, ps
	}
	idsA, papersA := makeBatch("a", 3)
	idsB, papersB := makeBatch("b", 5) // total 8 empty-refs across two batches
	stubResp := map[string][]Paper{"A": papersA, "B": papersB}
	currentLabel := ""
	inner := &stubRefLister{
		stubProvider: stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
			return stubResp[currentLabel], nil
		}},
		refs: func(ctx context.Context, id string, fields []string) ([]Paper, error) {
			return []Paper{{ExternalIDs: ExternalIDs{"DOI": "10.1/" + id}}}, nil
		},
	}
	r := &ResolvingTertiary{Inner: inner, Resolver: func(ctx context.Context, dois []string) ([]Paper, error) {
		out := make([]Paper, 0, len(dois))
		for _, d := range dois {
			out = append(out, Paper{PaperID: "W_" + d})
		}
		return out, nil
	}}
	ctx := WithRefsBackfillBudget(context.Background(), budget)
	currentLabel = "A"
	if _, err := r.GetPaperBatch(ctx, idsA, []string{"paperId", "references.paperId"}); err != nil {
		t.Fatal(err)
	}
	currentLabel = "B"
	if _, err := r.GetPaperBatch(ctx, idsB, []string{"paperId", "references.paperId"}); err != nil {
		t.Fatal(err)
	}
	if got := len(inner.refCalls); got != budget {
		t.Errorf("paginated /references calls across two batches: got %d, want %d (budget shared)", got, budget)
	}
}

// Without a budget attached the fallback is unbounded — preserve the
// pre-cap behaviour for non-Builder callers (CLI scripts, tests).
func TestResolvingTertiaryBatchPaginatedRefsUnboundedWithoutBudget(t *testing.T) {
	const inputs = 25
	ids := make([]string, inputs)
	stubPapers := make([]Paper, inputs)
	for i := 0; i < inputs; i++ {
		ids[i] = fmt.Sprintf("hex%d", i)
		stubPapers[i] = Paper{PaperID: ids[i]}
	}
	inner := &stubRefLister{
		stubProvider: stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
			return stubPapers, nil
		}},
		refs: func(ctx context.Context, id string, fields []string) ([]Paper, error) {
			return []Paper{{ExternalIDs: ExternalIDs{"DOI": "10.1/" + id}}}, nil
		},
	}
	r := &ResolvingTertiary{Inner: inner, Resolver: func(ctx context.Context, dois []string) ([]Paper, error) {
		out := make([]Paper, 0, len(dois))
		for _, d := range dois {
			out = append(out, Paper{PaperID: "W_" + d})
		}
		return out, nil
	}}
	if _, err := r.GetPaperBatch(context.Background(), ids, []string{"paperId", "references.paperId"}); err != nil {
		t.Fatal(err)
	}
	if got := len(inner.refCalls); got != inputs {
		t.Errorf("without budget all empty-refs should fall through, got %d / %d", got, inputs)
	}
}

// stubAr5iv satisfies ArxivRefsFetcher for testing the ar5iv cascade
// inside ResolvingTertiary.supplementRefsViaPagination.
type stubAr5iv struct {
	fn    func(ctx context.Context, arxivID string) ([]Paper, error)
	calls []string
}

func (s *stubAr5iv) GetReferences(ctx context.Context, arxivID string) ([]Paper, error) {
	s.calls = append(s.calls, arxivID)
	if s.fn == nil {
		return nil, nil
	}
	return s.fn(ctx, arxivID)
}

// AAAI / ICML papers commonly come back from S2 with publisher-elided
// references — paginated /references returns no useful entries. The
// tertiary must then reach for ar5iv to recover the bibliography from
// the LaTeX-rendered HTML and translate it through the existing
// resolver chain.
// For arxiv preprints we skip S2 paginated /references and go straight
// to ar5iv: the publisher-elision rate on the paginated endpoint is high
// enough that the round-trip is mostly wasted, and ar5iv lives on its
// own rate limiter. The pager must not be consulted at all when ar5iv
// supplies refs successfully.
func TestResolvingTertiarySkipsPaginatedForArxivPaperWithAr5iv(t *testing.T) {
	inner := &stubRefLister{
		stubProvider: stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{
				PaperID:     "S2HEX",
				ExternalIDs: ExternalIDs{"ArXiv": "2511.99999"},
			}, nil
		}},
		refs: func(ctx context.Context, id string, fields []string) ([]Paper, error) {
			t.Fatalf("paginated /references must not fire for arxiv preprints when ar5iv is wired")
			return nil, nil
		},
	}
	ar5iv := &stubAr5iv{
		fn: func(ctx context.Context, arxivID string) ([]Paper, error) {
			return []Paper{
				{ExternalIDs: ExternalIDs{"ArXiv": "2502.13923"}},
				{ExternalIDs: ExternalIDs{"ArXiv": "2503.17434"}},
			}, nil
		},
	}
	resolver := func(ctx context.Context, dois []string) ([]Paper, error) {
		out := make([]Paper, 0, len(dois))
		for _, d := range dois {
			out = append(out, Paper{PaperID: "W_" + d})
		}
		return out, nil
	}
	r := &ResolvingTertiary{Inner: inner, Resolver: resolver, Ar5iv: ar5iv}

	p, err := r.GetPaper(context.Background(), "ARXIV:2511.99999", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ar5iv.calls) != 1 || ar5iv.calls[0] != "2511.99999" {
		t.Fatalf("ar5iv must be called once with the seed arxiv id, got %v", ar5iv.calls)
	}
	if len(inner.refCalls) != 0 {
		t.Errorf("paginated /references must be skipped for arxiv preprints, got %d calls", len(inner.refCalls))
	}
	if len(p.References) != 2 {
		t.Errorf("want 2 translated refs from ar5iv, got %d: %+v", len(p.References), p.References)
	}
}

// When ar5iv returns empty for an arxiv preprint (rendering failure /
// missing page), we still fall back to S2 paginated rather than giving
// up — that's the original cascade that PR #30 introduced.
func TestResolvingTertiaryFallsBackToPaginatedWhenAr5ivEmptyForArxiv(t *testing.T) {
	inner := &stubRefLister{
		stubProvider: stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{
				PaperID:     "S2HEX",
				ExternalIDs: ExternalIDs{"ArXiv": "2511.99999"},
			}, nil
		}},
		refs: func(ctx context.Context, id string, fields []string) ([]Paper, error) {
			return []Paper{{ExternalIDs: ExternalIDs{"DOI": "10.1/x"}}}, nil
		},
	}
	ar5iv := &stubAr5iv{
		fn: func(ctx context.Context, arxivID string) ([]Paper, error) {
			return nil, nil
		},
	}
	resolver := func(ctx context.Context, dois []string) ([]Paper, error) {
		out := make([]Paper, 0, len(dois))
		for _, d := range dois {
			out = append(out, Paper{PaperID: "W_" + d})
		}
		return out, nil
	}
	r := &ResolvingTertiary{Inner: inner, Resolver: resolver, Ar5iv: ar5iv}

	p, err := r.GetPaper(context.Background(), "ARXIV:2511.99999", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ar5iv.calls) != 1 {
		t.Fatalf("ar5iv must be tried first, got %d calls", len(ar5iv.calls))
	}
	if len(inner.refCalls) != 1 {
		t.Errorf("S2 paginated must fall back when ar5iv comes back empty, got %d calls", len(inner.refCalls))
	}
	if len(p.References) != 1 {
		t.Errorf("want 1 ref from paginated fallback, got %d: %+v", len(p.References), p.References)
	}
}

func TestResolvingTertiaryAr5ivFallbackWhenPaginatedEmpty(t *testing.T) {
	inner := &stubRefLister{
		stubProvider: stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{
				PaperID:     "S2HEX",
				ExternalIDs: ExternalIDs{"ArXiv": "2511.99999"},
			}, nil
		}},
		refs: func(ctx context.Context, id string, fields []string) ([]Paper, error) {
			return nil, nil // S2 paginated returned empty (publisher elision)
		},
	}
	ar5iv := &stubAr5iv{
		fn: func(ctx context.Context, arxivID string) ([]Paper, error) {
			return []Paper{
				{ExternalIDs: ExternalIDs{"ArXiv": "2502.13923"}},
				{ExternalIDs: ExternalIDs{"ArXiv": "2503.17434"}},
			}, nil
		},
	}
	resolver := func(ctx context.Context, dois []string) ([]Paper, error) {
		out := make([]Paper, 0, len(dois))
		for _, d := range dois {
			out = append(out, Paper{PaperID: "W_" + d})
		}
		return out, nil
	}
	r := &ResolvingTertiary{Inner: inner, Resolver: resolver, Ar5iv: ar5iv}

	p, err := r.GetPaper(context.Background(), "ARXIV:2511.99999", []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ar5iv.calls) != 1 || ar5iv.calls[0] != "2511.99999" {
		t.Fatalf("ar5iv must be called once with the seed arxiv id, got %v", ar5iv.calls)
	}
	if len(p.References) != 2 {
		t.Errorf("want 2 translated refs from ar5iv, got %d: %+v", len(p.References), p.References)
	}
}

// For non-arxiv papers (DOI but no ArXiv id) the original ordering still
// applies: try S2 paginated first, fall back to ar5iv only when it
// returns nothing. Paying for ar5iv is wasted when paginated already
// produced refs, and arxivIDFromPaper returns "" so the ar5iv-first
// arxiv shortcut doesn't trigger here.
func TestResolvingTertiaryAr5ivSkippedWhenPaginatedHasRefsForNonArxiv(t *testing.T) {
	inner := &stubRefLister{
		stubProvider: stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{PaperID: "S2HEX", ExternalIDs: ExternalIDs{"DOI": "10.1/journal"}}, nil
		}},
		refs: func(ctx context.Context, id string, fields []string) ([]Paper, error) {
			return []Paper{{ExternalIDs: ExternalIDs{"DOI": "10.1/a"}}}, nil
		},
	}
	ar5iv := &stubAr5iv{
		fn: func(ctx context.Context, arxivID string) ([]Paper, error) {
			t.Fatalf("ar5iv must not fire for non-arxiv papers when paginated has refs")
			return nil, nil
		},
	}
	resolver := func(ctx context.Context, dois []string) ([]Paper, error) {
		return []Paper{{PaperID: "W_a"}}, nil
	}
	r := &ResolvingTertiary{Inner: inner, Resolver: resolver, Ar5iv: ar5iv}

	if _, err := r.GetPaper(context.Background(), "DOI:10.1/journal", []string{"paperId", "references.paperId"}); err != nil {
		t.Fatal(err)
	}
	if len(ar5iv.calls) != 0 {
		t.Fatalf("ar5iv called %d times, want 0", len(ar5iv.calls))
	}
	if len(inner.refCalls) != 1 {
		t.Errorf("paginated /references must be called once, got %d", len(inner.refCalls))
	}
}

// Without an arxiv id (no ArXiv ext, no arxiv DOI) ar5iv has nothing
// to look up — the cascade must skip it cleanly.
func TestResolvingTertiaryAr5ivSkippedWhenNoArxivID(t *testing.T) {
	inner := &stubRefLister{
		stubProvider: stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{PaperID: "S2HEX", ExternalIDs: ExternalIDs{"DOI": "10.1/journal"}}, nil
		}},
		refs: func(ctx context.Context, id string, fields []string) ([]Paper, error) {
			return nil, nil
		},
	}
	ar5iv := &stubAr5iv{
		fn: func(ctx context.Context, arxivID string) ([]Paper, error) {
			t.Fatalf("ar5iv must not fire when no arxiv id is derivable")
			return nil, nil
		},
	}
	r := &ResolvingTertiary{Inner: inner, Resolver: func(ctx context.Context, dois []string) ([]Paper, error) { return nil, nil }, Ar5iv: ar5iv}
	if _, err := r.GetPaper(context.Background(), "DOI:10.1/journal", []string{"paperId", "references.paperId"}); err != nil {
		t.Fatal(err)
	}
	if len(ar5iv.calls) != 0 {
		t.Fatalf("ar5iv called %d times, want 0", len(ar5iv.calls))
	}
}

// stubCiterProvider is a stubProvider that also satisfies citerLister.
type stubCiterProvider struct {
	stubProvider
	citersFrom func(ctx context.Context, id string, offset, limit int, fields []string) ([]Paper, error)
	citerCalls []citerCall
}

type citerCall struct {
	id     string
	offset int
	limit  int
}

func (s *stubCiterProvider) GetCitationsFrom(ctx context.Context, id string, offset, limit int, fields []string) ([]Paper, error) {
	s.citerCalls = append(s.citerCalls, citerCall{id: id, offset: offset, limit: limit})
	return s.citersFrom(ctx, id, offset, limit, fields)
}

func TestResolvingTertiarySupplementsCitersWhenInlineHits1000(t *testing.T) {
	inline := make([]Paper, 1000)
	for i := range inline {
		inline[i] = Paper{PaperID: "x" + strconv.Itoa(i), ExternalIDs: ExternalIDs{"DOI": "10.x/" + strconv.Itoa(i)}}
	}
	inner := &stubCiterProvider{
		stubProvider: stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{
				PaperID:       "S2HEX",
				CitationCount: 1200,
				Citations:     inline,
			}, nil
		}},
		citersFrom: func(ctx context.Context, id string, offset, limit int, fields []string) ([]Paper, error) {
			return []Paper{
				{PaperID: "x5", ExternalIDs: ExternalIDs{"DOI": "10.x/5"}}, // dup of inline
				{PaperID: "newA", ExternalIDs: ExternalIDs{"DOI": "10.new/a"}},
				{PaperID: "newB", ExternalIDs: ExternalIDs{"DOI": "10.new/b"}},
			}, nil
		},
	}

	resolvedCalls := 0
	r := &ResolvingTertiary{
		Inner: inner,
		Resolver: func(ctx context.Context, dois []string) ([]Paper, error) {
			resolvedCalls++
			out := make([]Paper, 0, len(dois))
			for i := range dois {
				out = append(out, Paper{PaperID: "W" + strconv.Itoa(i)})
			}
			return out, nil
		},
		CiterSupplementLimit: 500,
	}

	p, err := r.GetPaper(context.Background(), "S2HEX", []string{"paperId", "citations.paperId"})
	if err != nil || p == nil {
		t.Fatalf("unexpected err=%v paper=%v", err, p)
	}
	if len(inner.citerCalls) != 1 {
		t.Fatalf("expected 1 supplement call, got %d", len(inner.citerCalls))
	}
	call := inner.citerCalls[0]
	if call.offset != 1000 || call.limit != 500 {
		t.Fatalf("supplement offset/limit wrong: offset=%d limit=%d", call.offset, call.limit)
	}
	if resolvedCalls == 0 {
		t.Fatalf("resolver must be invoked on merged cites")
	}
}

func TestResolvingTertiaryDoesNotSupplementWhenInlineBelowCap(t *testing.T) {
	inner := &stubCiterProvider{
		stubProvider: stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{PaperID: "S2", CitationCount: 500, Citations: []Paper{{PaperID: "a", ExternalIDs: ExternalIDs{"DOI": "10.x/a"}}}}, nil
		}},
		citersFrom: func(ctx context.Context, id string, offset, limit int, fields []string) ([]Paper, error) {
			t.Fatalf("must not paginate when inline fits")
			return nil, nil
		},
	}
	r := &ResolvingTertiary{
		Inner:                inner,
		Resolver:             func(ctx context.Context, dois []string) ([]Paper, error) { return []Paper{{PaperID: "W1"}}, nil },
		CiterSupplementLimit: 500,
	}
	if _, err := r.GetPaper(context.Background(), "S2", []string{"paperId", "citations.paperId"}); err != nil {
		t.Fatal(err)
	}
	if len(inner.citerCalls) != 0 {
		t.Fatalf("expected no paginated calls, got %d", len(inner.citerCalls))
	}
}

func TestResolvingTertiaryDoesNotSupplementWhenLimitZero(t *testing.T) {
	inline := make([]Paper, 1000)
	for i := range inline {
		inline[i] = Paper{PaperID: "x" + strconv.Itoa(i), ExternalIDs: ExternalIDs{"DOI": "10.x/" + strconv.Itoa(i)}}
	}
	inner := &stubCiterProvider{
		stubProvider: stubProvider{getPaper: func(ctx context.Context, id string, fields []string) (*Paper, error) {
			return &Paper{PaperID: "S2", CitationCount: 2000, Citations: inline}, nil
		}},
		citersFrom: func(ctx context.Context, id string, offset, limit int, fields []string) ([]Paper, error) {
			t.Fatalf("must not paginate when CiterSupplementLimit is 0")
			return nil, nil
		},
	}
	r := &ResolvingTertiary{Inner: inner, Resolver: func(ctx context.Context, dois []string) ([]Paper, error) { return []Paper{{PaperID: "W1"}}, nil }}
	if _, err := r.GetPaper(context.Background(), "S2", []string{"paperId", "citations.paperId"}); err != nil {
		t.Fatal(err)
	}
	if len(inner.citerCalls) != 0 {
		t.Fatalf("expected no paginated calls, got %d", len(inner.citerCalls))
	}
}

func TestResolvingTertiaryRecommendTranslatesArxivAndDOIs(t *testing.T) {
	// S2 recommendations come back in S2 paperId space with ArXiv (and
	// sometimes DOI) externalIds. The tertiary must translate both into
	// primary-space W-IDs by synthesising 10.48550/arxiv.<id> when the rec
	// only has an ArXiv id, and dropping recs that have neither.
	inner := &stubProvider{
		recommend: func(ctx context.Context, id string, limit int, fields []string) ([]Paper, error) {
			return []Paper{
				{PaperID: "rec_doi", ExternalIDs: ExternalIDs{"DOI": "10.1/explicit"}},
				{PaperID: "rec_arxiv_only", ExternalIDs: ExternalIDs{"ArXiv": "2511.99001"}},
				{PaperID: "rec_arxiv_upper", ExternalIDs: ExternalIDs{"ArXiv": "2511.99002", "DOI": ""}}, // empty DOI shouldn't suppress arxiv synthesis
				{PaperID: "rec_neither"}, // dropped
			}, nil
		},
	}
	var resolved [][]string
	resolver := func(ctx context.Context, dois []string) ([]Paper, error) {
		resolved = append(resolved, append([]string(nil), dois...))
		out := make([]Paper, 0, len(dois))
		for _, d := range dois {
			out = append(out, Paper{PaperID: "W_" + d})
		}
		return out, nil
	}

	r := &ResolvingTertiary{Inner: inner, Resolver: resolver}
	got, err := r.Recommend(context.Background(), "SEED_HEX", 10, []string{"paperId", "title"})
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 translated recs, got %d: %+v", len(got), got)
	}
	if len(resolved) != 1 {
		t.Fatalf("want exactly 1 resolver call (batched), got %d: %v", len(resolved), resolved)
	}
	doiSet := map[string]bool{}
	for _, d := range resolved[0] {
		doiSet[d] = true
	}
	if !doiSet["10.1/explicit"] || !doiSet["10.48550/arxiv.2511.99001"] || !doiSet["10.48550/arxiv.2511.99002"] {
		t.Errorf("missing expected DOIs in resolver call: %v", resolved[0])
	}
}

func TestResolvingTertiaryRecommendNilOnNonRecommenderInner(t *testing.T) {
	// The Inner must implement Recommender for tertiary to forward calls. A
	// plain PaperProvider stub (no recommend func) should yield nil/empty so
	// the builder's fallback can short-circuit gracefully.
	inner := &stubProvider{} // no recommend func wired
	type onlyPaperProvider interface {
		Search(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error)
		GetPaper(ctx context.Context, id string, fields []string) (*Paper, error)
		GetPaperBatch(ctx context.Context, ids []string, fields []string) ([]Paper, error)
	}
	// Compile-time check that stubProvider satisfies the narrow interface;
	// the assertion at runtime in tertiary should not depend on this.
	var _ onlyPaperProvider = inner

	r := &ResolvingTertiary{Inner: paperProviderOnly{inner}}
	got, err := r.Recommend(context.Background(), "SEED", 10, nil)
	if err != nil {
		t.Fatalf("recommend on non-Recommender inner: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %d recs", len(got))
	}
}

// paperProviderOnly hides the embedded stubProvider's Recommend method so
// the type assertion to Recommender inside ResolvingTertiary.Recommend
// fails — modeling a real PaperProvider that lacks recommendations support.
type paperProviderOnly struct{ p PaperProvider }

func (p paperProviderOnly) Search(ctx context.Context, q string, limit int, fields []string) (*SearchResponse, error) {
	return p.p.Search(ctx, q, limit, fields)
}
func (p paperProviderOnly) GetPaper(ctx context.Context, id string, fields []string) (*Paper, error) {
	return p.p.GetPaper(ctx, id, fields)
}
func (p paperProviderOnly) GetPaperBatch(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
	return p.p.GetPaperBatch(ctx, ids, fields)
}

func TestResolvingTertiaryBatchTranslatesRefs(t *testing.T) {
	// Mirrors TestResolvingTertiaryTranslatesRefs but for the batch path:
	// hex refs from Inner must come back as primary-space W-IDs via one
	// deduped resolver call, and cites must be cleared (batch path skips
	// cite enrichment by design — see tertiary.go:142).
	inner := &stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
		return []Paper{
			{
				PaperID:     "hex_paper_1",
				ExternalIDs: ExternalIDs{"DOI": "10.1/p1"},
				References: []Paper{
					{PaperID: "hex_r1", ExternalIDs: ExternalIDs{"DOI": "10.1/a"}},
					{PaperID: "hex_r2", ExternalIDs: ExternalIDs{"DOI": "10.1/B"}}, // upper → lower
					{PaperID: "hex_r3"}, // no DOI → dropped
				},
				Citations: []Paper{{PaperID: "hex_c1", ExternalIDs: ExternalIDs{"DOI": "10.2/c"}}},
			},
			{
				PaperID:     "hex_paper_2",
				ExternalIDs: ExternalIDs{"DOI": "10.1/p2"},
				References: []Paper{
					{PaperID: "hex_r4", ExternalIDs: ExternalIDs{"DOI": "10.1/a"}}, // shared with paper_1
				},
			},
		}, nil
	}}

	var resolverCalls [][]string
	resolver := func(ctx context.Context, dois []string) ([]Paper, error) {
		resolverCalls = append(resolverCalls, append([]string(nil), dois...))
		out := make([]Paper, 0, len(dois))
		for _, d := range dois {
			out = append(out, Paper{PaperID: "W_" + d, ExternalIDs: ExternalIDs{"DOI": d}})
		}
		return out, nil
	}

	r := &ResolvingTertiary{Inner: inner, Resolver: resolver}
	papers, err := r.GetPaperBatch(context.Background(), []string{"DOI:10.1/p1", "DOI:10.1/p2"}, []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if len(papers) != 2 {
		t.Fatalf("expected 2 papers, got %d", len(papers))
	}

	if len(papers[0].References) != 2 {
		t.Fatalf("paper 1: expected 2 resolved refs (drop no-DOI), got %d: %+v", len(papers[0].References), papers[0].References)
	}
	wantPaper0 := map[string]bool{"W_10.1/a": false, "W_10.1/b": false}
	for _, ref := range papers[0].References {
		if _, ok := wantPaper0[ref.PaperID]; !ok {
			t.Fatalf("paper 1: unexpected ref %q", ref.PaperID)
		}
		wantPaper0[ref.PaperID] = true
	}
	for wid, seen := range wantPaper0 {
		if !seen {
			t.Fatalf("paper 1: missing expected ref %q", wid)
		}
	}
	if papers[0].Citations != nil {
		t.Fatalf("paper 1: citations must be cleared in batch path, got %+v", papers[0].Citations)
	}

	if len(papers[1].References) != 1 || papers[1].References[0].PaperID != "W_10.1/a" {
		t.Fatalf("paper 2: expected [W_10.1/a], got %+v", papers[1].References)
	}

	if len(resolverCalls) != 1 {
		t.Fatalf("batch must call resolver exactly once, got %d calls", len(resolverCalls))
	}
	if len(resolverCalls[0]) != 2 {
		t.Fatalf("resolver must see 2 deduped DOIs, got %v", resolverCalls[0])
	}

	if len(inner.batchCalls) != 1 {
		t.Fatalf("expected 1 inner batch call, got %d", len(inner.batchCalls))
	}
	gotFields := inner.batchCalls[0].fields
	if !slices.Contains(gotFields, "references.externalIds") {
		t.Fatalf("batch must enrich references.externalIds, got %v", gotFields)
	}
}

func TestResolvingTertiaryBatchDropsRefsWhenResolverNil(t *testing.T) {
	inner := &stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
		return []Paper{{PaperID: "h", References: []Paper{{PaperID: "hex", ExternalIDs: ExternalIDs{"DOI": "10.1/x"}}}}}, nil
	}}
	r := &ResolvingTertiary{Inner: inner} // no Resolver
	papers, err := r.GetPaperBatch(context.Background(), []string{"x"}, []string{"references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if papers[0].References != nil {
		t.Fatalf("refs must be cleared when resolver is nil, got %+v", papers[0].References)
	}
}

func TestHybridBatchSupplementsArxivPreprints(t *testing.T) {
	// The reason this path exists: OpenAlex returns referenced_works=[] for
	// most arxiv preprints (OpenVLA, π₀, DROID, CrossFormer, RDT-1B). Without
	// this supplement, biblio coupling collapses to 0 for every first-hop
	// candidate and the whole modern-robotics cluster falls out of top-40.
	primary := &stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
		return []Paper{
			{
				PaperID:     "W_arxiv_1",
				ExternalIDs: ExternalIDs{"DOI": "10.48550/arXiv.2406.09246"},
				// empty refs → should get supplemented
			},
			{
				PaperID:     "W_already_filled",
				ExternalIDs: ExternalIDs{"DOI": "10.1/already"},
				References:  []Paper{{PaperID: "W_ref_existing"}}, // non-empty → skip
			},
			{
				PaperID:     "W_no_doi",
				ExternalIDs: ExternalIDs{},
				// empty refs, no DOI → skip (can't translate)
			},
		}, nil
	}}

	var tertiaryIDs []string
	tertiary := &stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
		tertiaryIDs = append(tertiaryIDs, ids...)
		return []Paper{
			{
				PaperID:     "",
				ExternalIDs: ExternalIDs{"DOI": "10.48550/arxiv.2406.09246"}, // lowercased
				References: []Paper{
					{PaperID: "W_translated_ref_1"},
					{PaperID: "W_translated_ref_2"},
				},
			},
		}, nil
	}}

	h := &HybridClient{Primary: primary, Tertiary: tertiary}
	papers, err := h.GetPaperBatch(context.Background(), []string{"W_arxiv_1", "W_already_filled", "W_no_doi"}, []string{"paperId", "references.paperId"})
	if err != nil {
		t.Fatal(err)
	}
	if len(papers) != 3 {
		t.Fatalf("expected 3 papers, got %d", len(papers))
	}

	if len(papers[0].References) != 2 {
		t.Fatalf("arxiv preprint should have been supplemented to 2 refs, got %d", len(papers[0].References))
	}
	if papers[0].ReferenceCount < 2 {
		t.Fatalf("ReferenceCount should track supplemented list, got %d", papers[0].ReferenceCount)
	}

	if len(papers[1].References) != 1 || papers[1].References[0].PaperID != "W_ref_existing" {
		t.Fatalf("already-filled paper must not be touched, got %+v", papers[1].References)
	}

	if papers[2].References != nil {
		t.Fatalf("no-DOI paper must stay empty, got %+v", papers[2].References)
	}

	if len(tertiary.batchCalls) != 1 {
		t.Fatalf("tertiary must be called exactly once per batch, got %d calls", len(tertiary.batchCalls))
	}
	if len(tertiaryIDs) != 1 || tertiaryIDs[0] != "DOI:10.48550/arxiv.2406.09246" {
		t.Fatalf("tertiary should only see arxiv DOI with DOI: prefix, got %v", tertiaryIDs)
	}
}

func TestHybridBatchSkipsSupplementWhenRefsNotRequested(t *testing.T) {
	primary := &stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
		return []Paper{{PaperID: "W1", ExternalIDs: ExternalIDs{"DOI": "10.1/x"}}}, nil
	}}
	tertiary := &stubProvider{getBatch: func(ctx context.Context, ids []string, fields []string) ([]Paper, error) {
		t.Fatalf("tertiary must not be called when refs not requested")
		return nil, nil
	}}
	h := &HybridClient{Primary: primary, Tertiary: tertiary}
	if _, err := h.GetPaperBatch(context.Background(), []string{"W1"}, []string{"paperId", "title"}); err != nil {
		t.Fatal(err)
	}
}

func TestSecondaryLookupID(t *testing.T) {
	cases := []struct {
		name string
		p    *Paper
		orig string
		want string
	}{
		{"doi from paper", &Paper{ExternalIDs: ExternalIDs{"DOI": "10.1/x"}}, "W1", "DOI:10.1/x"},
		{"arxiv from paper", &Paper{ExternalIDs: ExternalIDs{"ArXiv": "2405.12213"}}, "W1", "arXiv:2405.12213"},
		{"doi prefers over arxiv", &Paper{ExternalIDs: ExternalIDs{"DOI": "10.1/x", "ArXiv": "y"}}, "W1", "DOI:10.1/x"},
		{"bare doi id", nil, "10.1/z", "DOI:10.1/z"},
		{"doi: prefix id", nil, "doi:10.1/z", "DOI:10.1/z"},
		{"arxiv: prefix id", nil, "arxiv:2405.x", "arXiv:2405.x"},
		{"https doi url", nil, "https://doi.org/10.1/z", "DOI:10.1/z"},
		{"no lookup possible", &Paper{}, "W1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := secondaryLookupID(c.p, c.orig); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
