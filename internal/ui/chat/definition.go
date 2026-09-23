package chat

import (
	"encoding/json"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// DefinitionToolRenderContext renders definition tool messages.
type DefinitionToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *DefinitionToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	if opts.IsPending() {
		return pendingToolView(sty, opts, ToolDisplayName(opts.ToolCall), "", width)
	}

	var params tools.DefinitionParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	header := toolHeader(sty, opts.Status, ToolDisplayName(opts.ToolCall), width, opts, params.Symbol)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	// Try to render code with syntax highlighting using metadata.
	var meta tools.DefinitionResponseMetadata
	if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err == nil && meta.Content != "" {
		body := toolOutputCodeContent(sty, meta.FilePath, meta.Content, 0, width, opts.ExpandedContent)
		return joinToolParts(header, body)
	}

	// Fallback to plain text.
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, width, opts.ExpandedContent))
	return joinToolParts(header, body)
}
