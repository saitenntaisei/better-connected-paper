-- HybridClient swaps a paper's References/Citations to Semantic Scholar
-- (40-char hex) paperIds when OpenAlex returned an empty refs list. Graphs
-- built before the swap reference an ID space that the new path won't emit,
-- and paper_links rows cached with OpenAlex-only refs/cites will keep the
-- Builder from ever triggering a supplement (it reads cached links first,
-- sees they're populated, and never goes back to the provider). Wipe all
-- three so the first post-migration build re-fetches through the hybrid
-- path end-to-end.
TRUNCATE TABLE paper_links;
TRUNCATE TABLE graphs;
UPDATE papers SET links_fetched_at = NULL;
