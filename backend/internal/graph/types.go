package graph

import (
	"time"

	"github.com/saitenntaisei/better-connected-paper/internal/citation"
)

// Node is a paper shown in the graph. Similarity is 0 for the seed, in [0,1] for others.
type Node struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	Abstract       string            `json:"abstract,omitempty"`
	Year           int               `json:"year,omitempty"`
	Venue          string            `json:"venue,omitempty"`
	Authors        []string          `json:"authors,omitempty"`
	CitationCount  int               `json:"citationCount,omitempty"`
	ReferenceCount int               `json:"referenceCount,omitempty"`
	ExternalIDs    map[string]string `json:"externalIds,omitempty"`
	URL            string            `json:"url,omitempty"`
	Similarity     float64           `json:"similarity"`
	IsSeed         bool              `json:"isSeed,omitempty"`
}

// EdgeKind enumerates edge semantics in the returned graph.
//
//   - EdgeCite      : directed citation A -> B (A cites B). Core "new" feature.
//   - EdgeSimilarity: optional undirected similarity link for layout hinting.
type EdgeKind string

const (
	EdgeCite       EdgeKind = "cite"
	EdgeSimilarity EdgeKind = "similarity"
)

// Edge connects two nodes. For "cite" edges, Source cites Target.
// For "similarity" edges, direction is arbitrary and Weight in [0,1].
type Edge struct {
	Source string   `json:"source"`
	Target string   `json:"target"`
	Kind   EdgeKind `json:"kind"`
	Weight float64  `json:"weight,omitempty"`
}

// Response is the full graph payload returned to the frontend.
type Response struct {
	Seed    Node      `json:"seed"`
	Nodes   []Node    `json:"nodes"`
	Edges   []Edge    `json:"edges"`
	BuiltAt time.Time `json:"builtAt"`
	// Preliminary marks a deferred-ar5iv fast response: the build
	// intentionally skipped bridges, link-supplements, and citer
	// supplements to land under 10 s, and a background goroutine is
	// re-running the full chain to replace this payload in the cache.
	// The HTTP handler skips its own graph-cache write when set so a
	// background failure can't leave the sparse payload permanently
	// cached; the frontend can also use this to display an
	// "enriching" indicator and poll for the enriched payload.
	Preliminary bool `json:"preliminary,omitempty"`
}

// ToNode lifts a citation.Paper into a graph Node. IsSeed/Similarity are
// set by the caller.
func ToNode(p citation.Paper) Node {
	authors := make([]string, 0, len(p.Authors))
	for _, a := range p.Authors {
		if a.Name != "" {
			authors = append(authors, a.Name)
		}
	}
	return Node{
		ID:             p.PaperID,
		Title:          p.Title,
		Abstract:       p.Abstract,
		Year:           p.Year,
		Venue:          p.Venue,
		Authors:        authors,
		CitationCount:  p.CitationCount,
		ReferenceCount: p.ReferenceCount,
		ExternalIDs:    map[string]string(p.ExternalIDs),
		URL:            p.URL,
	}
}
