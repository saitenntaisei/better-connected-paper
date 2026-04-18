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
		ExternalIDs:    p.ExternalIDs,
		URL:            p.URL,
	}
}
