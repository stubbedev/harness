package chat

import (
	"encoding/json"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// LSPRestartToolRenderContext renders lsprestart tool messages.
type LSPRestartToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *LSPRestartToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	if opts.IsPending() {
		return pendingToolView(sty, opts, ToolDisplayName(opts.ToolCall), "", width)
	}

	var params tools.LSPRestartParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	var toolParams []string
	if params.Name != "" {
		toolParams = append(toolParams, params.Name)
	}

	header := toolHeader(sty, opts.Status, ToolDisplayName(opts.ToolCall), width, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, width, opts.ExpandedContent))
	return joinToolParts(header, body)
}
