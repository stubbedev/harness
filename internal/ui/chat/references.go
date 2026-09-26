package chat

import (
	"encoding/json"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// ReferencesToolRenderContext renders references tool messages.
type ReferencesToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *ReferencesToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return renderStandardTool(sty, width, opts, opts.Name, func() ([]string, bool) {
		var params tools.ReferencesParams
		_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)
		toolParams := []string{params.Symbol}
		if params.Path != "" {
			toolParams = append(toolParams, "path", fsext.PrettyPath(params.Path))
		}
		return toolParams, true
	}, nil)
}
