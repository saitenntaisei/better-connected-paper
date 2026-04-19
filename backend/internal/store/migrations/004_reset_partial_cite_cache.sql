-- Any paper_links row written between the initial OpenAlex rollout and the
-- CitationsUnknown guard was stamped with links_fetched_at even when the
-- cited-by list was truncated at openAlexCitesLimit (100). Reads then served
-- that truncated prefix as authoritative and corrupted co-citation scoring.
-- graphs rows were materialized from that same partial data, so serving them
-- from cache would keep handing out structurally wrong results even after
-- the links are cleared. Wipe all three so the Builder recomputes end-to-end
-- through the fixed path.
TRUNCATE TABLE paper_links;
TRUNCATE TABLE graphs;
UPDATE papers SET links_fetched_at = NULL;
