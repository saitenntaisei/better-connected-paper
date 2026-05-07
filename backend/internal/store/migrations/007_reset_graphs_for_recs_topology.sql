-- Builder now augments sparse seeds (refs+cites < threshold) with S2
-- recommendations, exempts those rec ids from pruneOrphanNodes, and
-- layers an embedding-similarity edge layer (specter_v2 cosine, KNN top-K)
-- over the existing biblio/coCite edges. All three changes alter the
-- topology of any cached graph row, so wipe the response cache to force
-- recomputation. Graph rows have a 30-day TTL, so without this wipe
-- users would keep seeing the pre-recs 4-node topology for up to a
-- month. paper_links + papers remain valid and reusable.
TRUNCATE TABLE graphs;
