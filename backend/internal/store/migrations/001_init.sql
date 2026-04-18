-- Cached paper metadata fetched from Semantic Scholar.
CREATE TABLE IF NOT EXISTS papers (
    paper_id         TEXT PRIMARY KEY,
    title            TEXT NOT NULL,
    abstract         TEXT,
    year             INTEGER,
    venue            TEXT,
    authors          JSONB NOT NULL DEFAULT '[]'::jsonb,
    citation_count   INTEGER NOT NULL DEFAULT 0,
    reference_count  INTEGER NOT NULL DEFAULT 0,
    influential_cite INTEGER NOT NULL DEFAULT 0,
    external_ids     JSONB NOT NULL DEFAULT '{}'::jsonb,
    url              TEXT,
    fetched_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Built graphs keyed by seed paper id. The payload is the exact JSON shape
-- the frontend consumes so a cache hit is a single row read.
CREATE TABLE IF NOT EXISTS graphs (
    seed_id    TEXT PRIMARY KEY,
    payload    JSONB NOT NULL,
    built_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    ttl_until  TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS graphs_ttl_idx ON graphs (ttl_until);
