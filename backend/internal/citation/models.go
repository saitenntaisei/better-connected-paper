package citation

// Paper is the canonical shape returned by Semantic Scholar.
// JSON tags match S2 Graph API v1 responses.
// References/Citations are only populated when the caller asks for them
// (e.g. fields=references.paperId or citations.paperId).
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
	References      []Paper           `json:"references,omitempty"`
	Citations       []Paper           `json:"citations,omitempty"`
}

// RefIDs returns the paperIds of papers this paper cites.
func (p *Paper) RefIDs() []string { return idsOf(p.References) }

// CitedByIDs returns the paperIds of papers citing this paper.
func (p *Paper) CitedByIDs() []string { return idsOf(p.Citations) }

func idsOf(ps []Paper) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.PaperID != "" {
			out = append(out, p.PaperID)
		}
	}
	return out
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
