package api

import (
	"context"

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

// Deps groups everything a Handler needs. Any field can be nil in unit tests.
type Deps struct {
	S2      PaperClient
	DB      *store.DB
	Builder GraphBuilder
}
