package api

import (
	"context"

	"golang.org/x/sync/singleflight"

	"github.com/saitenntaisei/better-connected-paper/internal/citation"
	"github.com/saitenntaisei/better-connected-paper/internal/graph"
	"github.com/saitenntaisei/better-connected-paper/internal/store"
)

// GraphBuilder abstracts graph.Builder for testing.
type GraphBuilder interface {
	Build(ctx context.Context, seedID string) (*graph.Response, error)
}

// PaperClient is the subset of citation.Client used by handlers (for search + single-paper fetch).
type PaperClient interface {
	Search(ctx context.Context, query string, limit int, fields []string) (*citation.SearchResponse, error)
	GetPaper(ctx context.Context, id string, fields []string) (*citation.Paper, error)
}

// Deps groups everything a Handler needs. Any field can be nil in unit tests;
// the Build/Search/Paper handlers that would need them will self-describe
// the missing dependency via 503 responses.
type Deps struct {
	S2      PaperClient
	DB      *store.DB
	Builder GraphBuilder

	// SFlight coalesces concurrent S2 traffic per key so a burst of
	// duplicate requests (same seed graph build, same paper detail, same
	// search query) shares one upstream call. Nil means "no coalescing".
	SFlight *singleflight.Group

	// SearchCache memoizes /api/search responses for a short TTL so repeat
	// searches skip S2 entirely. Nil means "no cache".
	SearchCache *searchCache
}
