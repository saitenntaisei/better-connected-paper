//go:build integration
// +build integration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/saitenntaisei/better-connected-paper/internal/citation"
)

// Run with: go test -tags=integration ./internal/store/... (requires DATABASE_URL)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(db.Close)

	// clean fixture tables between runs
	_, _ = db.Pool.Exec(ctx, "TRUNCATE papers, graphs RESTART IDENTITY")
	return db
}

func TestUpsertAndGetPapers(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	papers := []citation.Paper{
		{
			PaperID: "P1", Title: "Alpha", Year: 2020,
			Authors:       []citation.Author{{Name: "Ada"}},
			CitationCount: 42,
			ExternalIDs:   citation.ExternalIDs{"DOI": "10.1/alpha"},
		},
		{PaperID: "P2", Title: "Beta"},
	}
	if err := db.UpsertPapers(ctx, papers); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := db.GetPapers(ctx, []string{"P1", "P2", "MISSING"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 papers, got %d", len(got))
	}

	single, err := db.GetPaper(ctx, "P1")
	if err != nil {
		t.Fatalf("get one: %v", err)
	}
	if single.Title != "Alpha" || single.CitationCount != 42 || single.ExternalIDs["DOI"] != "10.1/alpha" {
		t.Errorf("roundtrip mismatch: %+v", single)
	}

	if _, err := db.GetPaper(ctx, "NOPE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestGraphCache(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	body := json.RawMessage(`{"seed":"S","nodes":[]}`)
	if err := db.PutGraph(ctx, "S", body, time.Hour); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := db.GetGraph(ctx, "S")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var wantDoc, gotDoc any
	if err := json.Unmarshal(body, &wantDoc); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("decode got: %v", err)
	}
	if !reflect.DeepEqual(wantDoc, gotDoc) {
		t.Errorf("payload mismatch: got %s, want %s", got, body)
	}

	if err := db.InvalidateGraph(ctx, "S"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, err := db.GetGraph(ctx, "S"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound after invalidate, got %v", err)
	}
}

func TestGraphTTLExpiry(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// set ttl in the past via a direct write
	_, err := db.Pool.Exec(ctx,
		"INSERT INTO graphs (seed_id, payload, built_at, ttl_until) VALUES ('expired', '{}'::jsonb, now(), now() - interval '1 hour')",
	)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := db.GetGraph(ctx, "expired"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired entry should behave as not found, got %v", err)
	}
}
