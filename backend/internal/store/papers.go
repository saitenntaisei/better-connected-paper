package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/saitenntaisei/better-connected-paper/internal/citation"
)

// knownBogusWorkIDs holds OpenAlex Works whose referenced_works arrays
// are demonstrably corrupt upstream — running:
//
//	curl https://api.openalex.org/works/W4385430679?select=referenced_works
//
// lists W4385245566 ("MizAR 60 for Mizar 50") inside RT-1's references,
// even though the real paper is interactive theorem proving and the
// real cited_by_count is sub-100, not the 75670 OpenAlex returns. Same
// pattern for Aion Framework (W4292779060): inflated cited_by_count
// (14188) and false referenced_works edges in unrelated robotics
// papers. The runtime cap on rankingBonus (graph.cappedRankingBonus)
// already keeps these from surfacing as graph nodes, but filtering
// at the store boundary is also necessary: otherwise every fresh
// build re-fetches RT-1 from OpenAlex, sees MizAR in its refs, and
// writes the bogus edge back to paper_links, polluting 2-hop support
// counting on future graphs.
//
// Keep this list narrow and append-only. Each entry should be backed
// by an independent verification step (the OpenAlex direct query
// above, plus a sanity check against Semantic Scholar's view of the
// same paper).
var knownBogusWorkIDs = map[string]struct{}{
	"W4385245566": {}, // "MizAR 60 for Mizar 50" — math theorem proving; real cc ≤100
	"W4292779060": {}, // "Aion Framework: Dimensional Emergence of AI Consciousness..." — fringe physics
}

// IsKnownBogusWorkID reports whether id is on the upstream-corruption
// denylist. Exported so the build pipeline (or future admin tooling)
// can mirror the cache-write filter without re-declaring the list.
func IsKnownBogusWorkID(id string) bool {
	_, ok := knownBogusWorkIDs[id]
	return ok
}

// filterBogusIDs drops any IDs on the upstream-corruption denylist
// (knownBogusWorkIDs). Used by ReplacePaperLinks before persisting
// refs/cites so OpenAlex's bogus referenced_works entries can't make
// it into our cache. Returns the input slice unchanged when nothing
// is filtered so the common path avoids an allocation.
func filterBogusIDs(ids []string) []string {
	keep := true
	for _, id := range ids {
		if _, bad := knownBogusWorkIDs[id]; bad {
			keep = false
			break
		}
	}
	if keep {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, bad := knownBogusWorkIDs[id]; bad {
			continue
		}
		out = append(out, id)
	}
	return out
}

// GetPaper returns a single cached paper; ErrNotFound if missing.
func (db *DB) GetPaper(ctx context.Context, id string) (*citation.Paper, error) {
	papers, err := db.GetPapers(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	if len(papers) == 0 {
		return nil, ErrNotFound
	}
	p := papers[0]
	return &p, nil
}

// GetPapers returns cached papers matching any of the given ids (order not preserved).
// A nil DB returns nil/nil so callers can transparently skip the cache.
func (db *DB) GetPapers(ctx context.Context, ids []string) ([]citation.Paper, error) {
	if db == nil || db.Pool == nil || len(ids) == 0 {
		return nil, nil
	}
	const q = `
        SELECT paper_id, title, COALESCE(abstract, ''),
               COALESCE(year, 0), COALESCE(venue, ''),
               authors, citation_count, reference_count, influential_cite,
               external_ids, COALESCE(url, '')
        FROM papers
        WHERE paper_id = ANY($1)
    `
	rows, err := db.Pool.Query(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]citation.Paper, 0, len(ids))
	for rows.Next() {
		var p citation.Paper
		var authorsJSON, externalIDsJSON []byte
		if err := rows.Scan(
			&p.PaperID, &p.Title, &p.Abstract,
			&p.Year, &p.Venue,
			&authorsJSON, &p.CitationCount, &p.ReferenceCount, &p.InfluentialCite,
			&externalIDsJSON, &p.URL,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(authorsJSON, &p.Authors); err != nil {
			return nil, fmt.Errorf("decode authors for %s: %w", p.PaperID, err)
		}
		if err := json.Unmarshal(externalIDsJSON, &p.ExternalIDs); err != nil {
			return nil, fmt.Errorf("decode externalIds for %s: %w", p.PaperID, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertPapers writes papers to the cache, replacing existing rows. No-op when DB is nil.
func (db *DB) UpsertPapers(ctx context.Context, papers []citation.Paper) error {
	if db == nil || db.Pool == nil || len(papers) == 0 {
		return nil
	}
	const q = `
        INSERT INTO papers (
            paper_id, title, abstract, year, venue, authors,
            citation_count, reference_count, influential_cite,
            external_ids, url, fetched_at
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now())
        ON CONFLICT (paper_id) DO UPDATE SET
            title = EXCLUDED.title,
            abstract = EXCLUDED.abstract,
            year = EXCLUDED.year,
            venue = EXCLUDED.venue,
            authors = EXCLUDED.authors,
            citation_count = EXCLUDED.citation_count,
            reference_count = EXCLUDED.reference_count,
            influential_cite = EXCLUDED.influential_cite,
            external_ids = EXCLUDED.external_ids,
            url = EXCLUDED.url,
            fetched_at = now()
    `
	batch := &pgx.Batch{}
	for _, p := range papers {
		if p.PaperID == "" {
			continue
		}
		if _, bogus := knownBogusWorkIDs[p.PaperID]; bogus {
			// Skip upstream-corrupt OpenAlex Works (MizAR / Aion) so a
			// fresh fetch can't re-pollute the cache. See knownBogusWorkIDs.
			continue
		}
		authorsJSON, err := json.Marshal(p.Authors)
		if err != nil {
			return err
		}
		externalIDsJSON, err := json.Marshal(p.ExternalIDs)
		if err != nil {
			return err
		}
		if len(authorsJSON) == 0 {
			authorsJSON = []byte("[]")
		}
		if len(externalIDsJSON) == 0 {
			externalIDsJSON = []byte("{}")
		}
		batch.Queue(q,
			p.PaperID, p.Title, nullable(p.Abstract), nullableInt(p.Year), nullable(p.Venue),
			authorsJSON, p.CitationCount, p.ReferenceCount, p.InfluentialCite,
			externalIDsJSON, nullable(p.URL),
		)
	}
	res := db.Pool.SendBatch(ctx, batch)
	defer res.Close()
	for range batch.Len() {
		if _, err := res.Exec(); err != nil {
			return err
		}
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(i int) any {
	if i == 0 {
		return nil
	}
	return i
}

// compile-time sanity check
var _ = errors.Is
