-- Per-paper reference/citation id list. Separated from papers so a paper
-- row can exist without links (e.g. fetched via /api/paper/{id}) while a
-- graph-build row has the full link graph cached alongside.
CREATE TABLE IF NOT EXISTS paper_links (
    paper_id  TEXT NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('ref','cite')),
    target_id TEXT NOT NULL,
    PRIMARY KEY (paper_id, direction, target_id)
);

CREATE INDEX IF NOT EXISTS paper_links_paper_direction_idx
    ON paper_links (paper_id, direction);

-- Marker that this row's links have been persisted into paper_links.
-- NULL means "links unknown, Builder must fall back to S2".
ALTER TABLE papers
    ADD COLUMN IF NOT EXISTS links_fetched_at TIMESTAMPTZ NULL;
