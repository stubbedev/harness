package chat

import (
	"encoding/json"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// ReplaceSymbolToolRenderContext renders replace symbol tool messages.
type ReplaceSymbolToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *ReplaceSymbolToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	if opts.IsPending() {
		return pendingToolView(sty, opts, ToolDisplayName(opts.ToolCall), "", width)
	}

	var params tools.ReplaceSymbolParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	file := fsext.PrettyPath(params.FilePath)
	header := toolHeader(sty, opts.Status, ToolDisplayName(opts.ToolCall), width, opts, params.Symbol, file)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}

	if !opts.HasResult() {
		return header
	}

	// Try to render as a diff using metadata.
	var meta tools.ReplaceSymbolResponseMetadata
	if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err == nil && (meta.OldContent != "" || meta.NewContent != "") {
		diff := toolOutputDiffContent(sty, file, meta.OldContent, meta.NewContent, width, opts.ExpandedContent)

		// On error, show error above the diff.
		if opts.Result.IsError {
			errLine := toolErrorContent(sty, opts.Result, width)
			return joinToolParts(header, errLine+"\n"+diff)
		}

		return joinToolParts(header, diff)
	}

	// Fallback to plain text if no metadata.
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, width, opts.ExpandedContent))
	return joinToolParts(header, body)
}
