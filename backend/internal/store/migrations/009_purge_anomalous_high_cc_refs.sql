-- Purge upstream-corrupt OpenAlex paper_links edges.
--
-- Two OpenAlex Works carry verifiably-inflated cited_by_count values
-- and appear inside the referenced_works arrays of unrelated
-- robotics/CV papers. The corruption is upstream — verified by direct
-- query, e.g.:
--
--   curl https://api.openalex.org/works/W4385430679?select=referenced_works
--
-- returns W4385245566 inside RT-1's referenced_works list, which the
-- actual paper definitely does not cite (it is a Mizar interactive-
-- theorem-proving paper from ITP 2023).
--
-- Bogus targets:
--   W4385245566  "MizAR 60 for Mizar 50"  cited_by_count=75670 (real ≤100)
--   W4292779060  "Aion Framework: Dimensional Emergence of AI Consciousness..."
--                                          cited_by_count=14188 (fringe)
--
-- The runtime cap on rankingBonus (cappedRankingBonus = struct × 4 in
-- internal/graph/similarity.go) already keeps these from surfacing as
-- top-MaxNodes nodes, but the wrong edges still occupy candidate slots
-- and pollute 2-hop support counting. Drop both the bogus papers and
-- every ref edge pointing at them so they can't re-enter via cache.
--
-- Future-proofing: store.UpsertPapers / store.ReplacePaperLinks now
-- filter against a hard-coded denylist (see knownBogusWorkIDs in
-- internal/store/papers.go) so even if a fresh OpenAlex fetch
-- re-emits these targets, they never reach the cache.

DELETE FROM paper_links
WHERE direction = 'ref'
  AND target_id IN ('W4385245566', 'W4292779060');

DELETE FROM papers
WHERE paper_id IN ('W4385245566', 'W4292779060');

-- Invalidate any cached graphs that may have included these as nodes
-- so the next request rebuilds against the cleaner state.
DELETE FROM graphs;
