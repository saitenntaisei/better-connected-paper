package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/saitenntaisei/better-connected-paper/internal/citation"
)

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
func (db *DB) GetPapers(ctx context.Context, ids []string) ([]citation.Paper, error) {
	if len(ids) == 0 {
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

// UpsertPapers writes papers to the cache, replacing existing rows.
func (db *DB) UpsertPapers(ctx context.Context, papers []citation.Paper) error {
	if len(papers) == 0 {
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
