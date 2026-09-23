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
	return renderStandardTool(sty, width, opts, ToolDisplayName(opts.ToolCall), func() ([]string, bool) {
		var params tools.CallHierarchyParams
		_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)
		direction := "incoming"
		if params.Direction == "outgoing" {
			direction = "outgoing"
		}
		return []string{params.Symbol, direction}, true
	}, nil)
}
