-- The ranking algorithm now scores 2-hop bridges on the same scale as
-- first-hop neighbors (biblio coupling + co-citation + direct + support
-- ratio), so a given seed produces a different graph than the one stored
-- under the old half-weight bridge rule. Graph rows have a 30-day TTL, so
-- without this wipe users would keep seeing the old topology for up to a
-- month. paper_links rows are still valid — only the per-seed cached
-- response needs to be re-materialized.
TRUNCATE TABLE graphs;
