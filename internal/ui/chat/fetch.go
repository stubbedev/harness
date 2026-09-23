package chat

import (
	"encoding/json"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// -----------------------------------------------------------------------------
// Fetch Tool
// -----------------------------------------------------------------------------

// FetchToolRenderContext renders fetch tool messages.
type FetchToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (f *FetchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	var params tools.FetchParams
	return renderStandardTool(sty, width, opts, ToolDisplayName(opts.ToolCall), func() ([]string, bool) {
		if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
			return nil, false
		}
		toolParams := []string{params.URL}
		if params.Format != "" {
			toolParams = append(toolParams, "format", params.Format)
		}
		if params.Timeout != 0 {
			toolParams = append(toolParams, "timeout", formatTimeout(params.Timeout))
		}
		return toolParams, true
	}, func() string {
		// Determine file extension for syntax highlighting based on format.
		file := getFileExtensionForFormat(params.Format)
		return toolOutputCodeContent(sty, file, opts.Result.Content, 0, width, opts.ExpandedContent)
	})
}

// getFileExtensionForFormat returns a filename with appropriate extension for syntax highlighting.
func getFileExtensionForFormat(format string) string {
	switch format {
	case "text":
		return "fetch.txt"
	case "html":
		return "fetch.html"
	default:
		return "fetch.md"
	}
}

// -----------------------------------------------------------------------------
// WebSearch Tool
// -----------------------------------------------------------------------------

// WebSearchToolRenderContext renders web_search tool messages.
type WebSearchToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (w *WebSearchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return renderStandardTool(sty, width, opts, ToolDisplayName(opts.ToolCall), func() ([]string, bool) {
		var params tools.WebSearchParams
		if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
			return nil, false
		}
		return []string{params.Query}, true
	}, func() string {
		return toolOutputMarkdownContent(sty, opts.Result.Content, width, opts.ExpandedContent)
	})
}
