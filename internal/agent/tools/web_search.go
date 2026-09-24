package tools

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
)

//go:embed web_search.md.tpl
var webSearchDescriptionTmpl []byte

var webSearchDescriptionTpl = template.Must(
	template.New("webSearchDescription").
		Parse(string(webSearchDescriptionTmpl)),
)

// NewWebSearchTool creates the web search tool. It is a read-only tool
// like any other: sub-agents get it because it is read-only, not because
// it is theirs.
func NewWebSearchTool(client *http.Client) fantasy.AgentTool {
	if client == nil {
		client = DefaultHTTPClient()
	}

	return fantasy.NewParallelAgentTool(
		WebSearchToolName,
		renderToolDescription(webSearchDescriptionTpl),
		func(ctx context.Context, params WebSearchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Query == "" {
				return fantasy.NewTextErrorResponse("query is required"), nil
			}

			maxResults := params.MaxResults
			if maxResults <= 0 {
				maxResults = 10
			}
			if maxResults > 20 {
				maxResults = 20
			}

			if err := maybeDelaySearch(ctx); err != nil {
				return fantasy.NewTextErrorResponse("Search cancelled: " + err.Error()), nil
			}
			results, err := searchDuckDuckGo(ctx, client, params.Query, maxResults)
			slog.Debug("Web search completed", "query", params.Query, "results", len(results), "err", err)
			if err != nil {
				return fantasy.NewTextErrorResponse("Failed to search: " + err.Error()), nil
			}

			out := formatSearchResults(results)
			if params.FetchTop > 0 && len(results) > 0 {
				top := results[:min(params.FetchTop, fetchTopMax, len(results))]
				out += "\n" + fetchTopResults(ctx, client, GetSessionFromContext(ctx), top)
			}
			return fantasy.NewTextResponse(out), nil
		},
	)
}

const (
	// fetchTopMax bounds fetch_top: the pages are fetched in parallel and
	// each costs context, so a handful is what a search can carry.
	fetchTopMax = 3
	// fetchTopTimeout is the per-page budget, so one slow site cannot
	// hold the search results hostage.
	fetchTopTimeout = 20 * time.Second
	// fetchTopPreviewBytes is how much of each page comes back inline.
	fetchTopPreviewBytes = 1500
)

// fetchTopResults fetches the given results concurrently and renders a
// preview of each. A page that fails reports its error in place; the
// search results themselves are never lost to a bad page.
func fetchTopResults(ctx context.Context, client *http.Client, sessionID string, results []SearchResult) string {
	previews := make([]string, len(results))
	var wg sync.WaitGroup
	for i, result := range results {
		wg.Go(func() {
			previews[i] = fetchResultPreview(ctx, client, sessionID, result)
		})
	}
	wg.Wait()

	var b strings.Builder
	fmt.Fprintf(&b, "Top %d pages:\n\n", len(results))
	b.WriteString(strings.Join(previews, "\n\n"))
	return b.String()
}

// fetchResultPreview renders one search result's page: its title and URL,
// the first fetchTopPreviewBytes of markdown, and the path of the whole
// page when it was long enough to spill.
func fetchResultPreview(ctx context.Context, client *http.Client, sessionID string, result SearchResult) string {
	ctx, cancel := context.WithTimeout(ctx, fetchTopTimeout)
	defer cancel()

	var b strings.Builder
	fmt.Fprintf(&b, "### %d. %s\nURL: %s\n", result.Position, result.Title, result.Link)

	res, err := FetchURL(ctx, client, result.Link, FetchFormatMarkdown)
	if err != nil {
		fmt.Fprintf(&b, "Error: %s", err)
		return b.String()
	}
	if res.Binary {
		fmt.Fprintf(&b, "Not text (%s, %d bytes); call fetch with \"download\": true to save it.", contentTypeOrUnknown(res.ContentType), res.Size)
		return b.String()
	}

	if len(res.Content) > MaxFetchSize {
		if path, err := spillFetchedContent(sessionID, res.Content, FetchFormatMarkdown); err == nil {
			fmt.Fprintf(&b, "Full page (%d bytes) saved to: %s\n", len(res.Content), path)
		}
	}
	preview := toolResultPrefix(res.Content, fetchTopPreviewBytes)
	b.WriteString(preview)
	if len(res.Content) > len(preview) {
		fmt.Fprintf(&b, "\n[... %d more bytes; fetch the URL for the rest]", len(res.Content)-len(preview))
	}
	return b.String()
}
