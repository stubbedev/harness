package chat

import (
	"encoding/json"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// CallHierarchyToolRenderContext renders call hierarchy tool messages.
type CallHierarchyToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *CallHierarchyToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	if opts.IsPending() {
		return pendingToolView(sty, opts, ToolDisplayName(opts.ToolCall), "", width)
	}

	var params tools.CallHierarchyParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	direction := "incoming"
	if params.Direction == "outgoing" {
		direction = "outgoing"
	}
	header := toolHeader(sty, opts.Status, ToolDisplayName(opts.ToolCall), width, opts, params.Symbol, direction)
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
