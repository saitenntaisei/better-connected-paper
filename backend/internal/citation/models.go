package citation

// Paper is the canonical shape returned by Semantic Scholar.
// JSON tags match S2 Graph API v1 responses.
type Paper struct {
	PaperID         string            `json:"paperId"`
	Title           string            `json:"title"`
	Abstract        string            `json:"abstract,omitempty"`
	Year            int               `json:"year,omitempty"`
	Venue           string            `json:"venue,omitempty"`
	Authors         []Author          `json:"authors,omitempty"`
	CitationCount   int               `json:"citationCount,omitempty"`
	ReferenceCount  int               `json:"referenceCount,omitempty"`
	InfluentialCite int               `json:"influentialCitationCount,omitempty"`
	ExternalIDs     map[string]string `json:"externalIds,omitempty"`
	URL             string            `json:"url,omitempty"`
}

// Author identifies a contributor on a paper.
type Author struct {
	AuthorID string `json:"authorId,omitempty"`
	Name     string `json:"name"`
}

// SearchResponse is what /paper/search returns.
type SearchResponse struct {
	Total  int     `json:"total"`
	Offset int     `json:"offset"`
	Next   int     `json:"next,omitempty"`
	Data   []Paper `json:"data"`
}

// Reference pairs a paper with the paper it cites.
// /paper/{id}/references returns objects keyed by "citedPaper".
type Reference struct {
	CitedPaper Paper `json:"citedPaper"`
}

// Citation pairs a paper with the paper citing it.
// /paper/{id}/citations returns objects keyed by "citingPaper".
type Citation struct {
	CitingPaper Paper `json:"citingPaper"`
}

// ListResponse wraps paginated reference/citation lists.
type ListResponse[T any] struct {
	Offset int `json:"offset"`
	Next   int `json:"next,omitempty"`
	Data   []T `json:"data"`
}

// DefaultPaperFields covers everything the frontend + graph builder needs.
var DefaultPaperFields = []string{
	"paperId", "title", "abstract", "year", "venue",
	"authors", "citationCount", "referenceCount",
	"influentialCitationCount", "externalIds", "url",
}

// ReferenceFields nests paper fields under citedPaper.
func ReferenceFields() []string { return nestedFields("references") }

// CitationFields nests paper fields under citingPaper.
func CitationFields() []string { return nestedFields("citations") }

func nestedFields(kind string) []string {
	prefix := "citedPaper"
	if kind == "citations" {
		prefix = "citingPaper"
	}
	out := make([]string, len(DefaultPaperFields))
	for i, f := range DefaultPaperFields {
		out[i] = prefix + "." + f
	}
	return out
}
