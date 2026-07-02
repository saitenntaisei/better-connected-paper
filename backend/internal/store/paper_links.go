package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/saitenntaisei/better-connected-paper/internal/citation"
)

// GetPaperLinks loads the ref/cite id lists for the given paperIDs that
// have their links_fetched_at set. Papers without persisted links are
// absent from the returned map so the caller can treat them as cache
// misses.
func (db *DB) GetPaperLinks(ctx context.Context, paperIDs []string) (map[string]paperLinks, error) {
	if db == nil || db.Pool == nil || len(paperIDs) == 0 {
		return nil, nil
	}
	const q = `
        SELECT pl.paper_id, pl.direction, pl.target_id
        FROM paper_links pl
        JOIN papers p ON p.paper_id = pl.paper_id AND p.links_fetched_at IS NOT NULL
        WHERE pl.paper_id = ANY($1)
    `
	rows, err := db.Pool.Query(ctx, q, paperIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]paperLinks)
	for rows.Next() {
		var pid, dir, tid string
		if err := rows.Scan(&pid, &dir, &tid); err != nil {
			return nil, err
		}
		links := out[pid]
		switch dir {
		case "ref":
			links.Refs = append(links.Refs, tid)
		case "cite":
			links.Cites = append(links.Cites, tid)
		}
		out[pid] = links
	}
	return out, rows.Err()
}

// ReplacePaperLinks rewrites the link rows for paperID in one transaction
// and stamps papers.links_fetched_at = now(). The caller is expected to
// have upserted the papers row already (via UpsertPapers).
//
// Refs and cites are filtered through filterBogusIDs first: OpenAlex's
// `referenced_works` arrays occasionally contain upstream-corrupt
// targets (verified MizAR / Aion cases — see knownBogusWorkIDs), and
// persisting those edges would let future builds see them as 2-hop
// bridge candidates regardless of how the graph scorer is tuned.
func (db *DB) ReplacePaperLinks(ctx context.Context, paperID string, refs, cites []string) error {
	if db == nil || db.Pool == nil || paperID == "" {
		return nil
	}
	if _, bogus := knownBogusWorkIDs[paperID]; bogus {
		// Don't persist links FROM a known-bogus paper either; the
		// caller (UpsertPapers) already skips inserting the row.
		return nil
	}
	refs = filterBogusIDs(refs)
	cites = filterBogusIDs(cites)
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "DELETE FROM paper_links WHERE paper_id = $1", paperID); err != nil {
		return err
	}

	batch := &pgx.Batch{}
	seen := make(map[[2]string]struct{}, len(refs)+len(cites))
	queueLink := func(direction, target string) {
		if target == "" {
			return
		}
		key := [2]string{direction, target}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		batch.Queue(
			"INSERT INTO paper_links (paper_id, direction, target_id) VALUES ($1,$2,$3)",
			paperID, direction, target,
		)
	}
	for _, r := range refs {
		queueLink("ref", r)
	}
	for _, c := range cites {
		queueLink("cite", c)
	}
	if batch.Len() > 0 {
		br := tx.SendBatch(ctx, batch)
		for range batch.Len() {
			if _, err := br.Exec(); err != nil {
				br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, "UPDATE papers SET links_fetched_at = now() WHERE paper_id = $1", paperID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// paperLinks bundles the two directed id lists returned by GetPaperLinks.
type paperLinks struct {
	Refs  []string
	Cites []string
}

// GetPapersWithLinks is GetPapers + hydration of References/Citations from
// the paper_links table. Papers whose links were never persisted come back
// with nil Reference/Citation slices so the caller can treat them as a
// cache miss. Used by the graph Builder.
func (db *DB) GetPapersWithLinks(ctx context.Context, ids []string) ([]citation.Paper, error) {
	papers, err := db.GetPapers(ctx, ids)
	if err != nil || len(papers) == 0 {
		return papers, err
	}
	wantLinks := make([]string, 0, len(papers))
	for _, p := range papers {
		wantLinks = append(wantLinks, p.PaperID)
	}
	links, err := db.GetPaperLinks(ctx, wantLinks)
	if err != nil {
		return nil, err
	}
	for i := range papers {
		if l, ok := links[papers[i].PaperID]; ok {
			attachLinks(&papers[i], l)
		}
	}
	return papers, nil
}

// attachLinks hydrates p.References / p.Citations from a paperLinks
// entry so callers can use Paper.RefIDs() / CitedByIDs() identically to
// the S2-fetched shape.
func attachLinks(p *citation.Paper, links paperLinks) {
	if len(links.Refs) > 0 {
		p.References = make([]citation.Paper, 0, len(links.Refs))
		for _, id := range links.Refs {
			p.References = append(p.References, citation.Paper{PaperID: id})
		}
	}
	if len(links.Cites) > 0 {
		p.Citations = make([]citation.Paper, 0, len(links.Cites))
		for _, id := range links.Cites {
			p.Citations = append(p.Citations, citation.Paper{PaperID: id})
		}
	}
}
