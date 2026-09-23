package chat

import (
	"encoding/json"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// RenameToolRenderContext renders rename tool messages.
type RenameToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *RenameToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return renderStandardTool(sty, width, opts, ToolDisplayName(opts.ToolCall), func() ([]string, bool) {
		var params tools.RenameParams
		_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)
		toolParams := []string{params.Symbol + " → " + params.NewName}
		if params.Path != "" {
			toolParams = append(toolParams, "path", fsext.PrettyPath(params.Path))
		}
		return toolParams, true
	}, nil)
}
