package chat

import (
	"encoding/json"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// SymbolsToolRenderContext renders symbols tool messages.
type SymbolsToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *SymbolsToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	var params tools.SymbolsParams
	return renderStandardTool(sty, width, opts, opts.Name, func() ([]string, bool) {
		_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)
		return []string{params.FilePath}, true
	}, func() string {
		// Render as code to preserve tree indentation.
		return toolOutputCodeContent(sty, params.FilePath, opts.Result.Content, 0, width, opts.ExpandedContent)
	})
}
