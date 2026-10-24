package mcpsrv

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/policy"
)

// DocsSearchInput is the query of the documentation search.
type DocsSearchInput struct {
	// Query is the text to look for.
	Query string `json:"query" jsonschema:"The words to look for, for example \"secret\" or \"error code\". An empty query lists the documentation."`
	// Limit caps the number of answers. The node returns at most ten.
	Limit int `json:"limit,omitempty" jsonschema:"The greatest number of sections to return. Leave it empty for the default of five."`
}

// DocsSearchResult is one section of the documentation.
type DocsSearchResult struct {
	// URI is the resource that serves the section.
	URI string `json:"uri"`
	// Title is the name of the section.
	Title string `json:"title"`
	// Excerpt is the beginning of the section.
	Excerpt string `json:"excerpt"`
}

// DocsSearchOutput carries the sections that match a query.
type DocsSearchOutput struct {
	// Query is the text that was searched for.
	Query string `json:"query"`
	// Results are the matching sections, the best match first.
	Results []DocsSearchResult `json:"results"`
}

// addDocsTool registers the documentation search.
func (s *Server) addDocsTool(server *mcp.Server, actor policy.Actor) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "docs.search",
		Description: "Search the documentation of the node: the quickstart, the security " +
			"model, the error codes and the documentation of every published template. " +
			"Read the documentation before an unfamiliar task, and read the error codes " +
			"when a call fails.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DocsSearchInput) (*mcp.CallToolResult, DocsSearchOutput, error) {
		if failure := requireScope(actor, policy.ScopeDocsRead, correlationIDFrom(ctx)); failure != nil {
			return nil, DocsSearchOutput{}, failure
		}
		query := strings.TrimSpace(input.Query)
		limit := input.Limit
		if limit <= 0 {
			limit = 5
		}
		if limit > 10 {
			limit = 10
		}
		sections := searchDocs(s.documentationCorpus(), query, limit)
		results := make([]DocsSearchResult, 0, len(sections))
		for _, section := range sections {
			results = append(results, DocsSearchResult{
				URI:     section.URI,
				Title:   section.Title,
				Excerpt: excerpt(section, 320),
			})
		}
		return nil, DocsSearchOutput{Query: query, Results: results}, nil
	})
}
