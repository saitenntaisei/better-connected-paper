-- Switching the default citation provider from Semantic Scholar (sha1-style
-- paperIds) to OpenAlex (W-prefixed ids) makes every cached row's paper_id
-- namespace incompatible. Wipe the caches so the new provider starts clean.
TRUNCATE TABLE papers CASCADE;
TRUNCATE TABLE paper_links CASCADE;
TRUNCATE TABLE graphs CASCADE;
