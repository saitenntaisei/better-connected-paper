-- The new ar5iv refs fallback only runs on provider fetches, but
-- Builder.fetchWithCache treats any cached row with refs OR cites as
-- complete — papers persisted from the old chain with cite links but no
-- ref links would bypass the new backfill indefinitely. Force them
-- back through the provider chain so ar5iv (and the paginated /references
-- fallback) gets one chance to populate the missing refs after deploy.
DELETE FROM paper_links
WHERE paper_id IN (
    SELECT DISTINCT paper_id FROM paper_links WHERE direction = 'cite'
    EXCEPT
    SELECT DISTINCT paper_id FROM paper_links WHERE direction = 'ref'
);
UPDATE papers
SET links_fetched_at = NULL
WHERE links_fetched_at IS NOT NULL
  AND paper_id NOT IN (SELECT DISTINCT paper_id FROM paper_links WHERE direction = 'ref');

-- Wipe the response cache too: a graphs row materialised from cite-only
-- paper_links carries the pre-ar5iv topology even after the link cache
-- itself is cleared, since the JSON payload is the source of truth for
-- /api/graph/{seedId} reads (30-day TTL).
TRUNCATE TABLE graphs;
