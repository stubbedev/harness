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
	return renderStandardTool(sty, width, opts, opts.Name, func() ([]string, bool) {
		var params tools.LSPRestartParams
		_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)
		if params.Name == "" {
			return nil, true
		}
		return []string{params.Name}, true
	}, nil)
}
