package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultGraphTTL is how long a built graph stays in the cache.
const DefaultGraphTTL = 30 * 24 * time.Hour

// GetGraph returns the cached JSON payload if it has not expired.
// A nil DB short-circuits to ErrNotFound so handlers can treat the cache as optional.
func (db *DB) GetGraph(ctx context.Context, seedID string) (json.RawMessage, error) {
	if db == nil || db.Pool == nil {
		return nil, ErrNotFound
	}
	const q = `
        SELECT payload FROM graphs
        WHERE seed_id = $1 AND ttl_until > now()
    `
	var payload []byte
	if err := db.Pool.QueryRow(ctx, q, seedID).Scan(&payload); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return payload, nil
}

// PutGraph stores the payload with a fresh TTL. No-op when DB is nil.
func (db *DB) PutGraph(ctx context.Context, seedID string, payload json.RawMessage, ttl time.Duration) error {
	if db == nil || db.Pool == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultGraphTTL
	}
	const q = `
        INSERT INTO graphs (seed_id, payload, built_at, ttl_until)
        VALUES ($1, $2, now(), now() + $3::interval)
        ON CONFLICT (seed_id) DO UPDATE SET
            payload = EXCLUDED.payload,
            built_at = now(),
            ttl_until = EXCLUDED.ttl_until
    `
	_, err := db.Pool.Exec(ctx, q, seedID, payload, ttl.String())
	return err
}

// InvalidateGraph removes a cached graph.
func (db *DB) InvalidateGraph(ctx context.Context, seedID string) error {
	if db == nil || db.Pool == nil {
		return nil
	}
	_, err := db.Pool.Exec(ctx, "DELETE FROM graphs WHERE seed_id = $1", seedID)
	return err
}

// StoreGraph is the graph.Cache-shaped wrapper around PutGraph: takes a
// pre-serialised payload and applies DefaultGraphTTL internally so the
// graph package doesn't have to import this one just to learn the TTL.
// Used by the deferred-ar5iv background goroutine to replace the
// initial sparse response with the enriched build so the next request
// serves the full graph from the cache instead of rebuilding.
func (db *DB) StoreGraph(ctx context.Context, seedID string, payload []byte) error {
	return db.PutGraph(ctx, seedID, json.RawMessage(payload), DefaultGraphTTL)
}
