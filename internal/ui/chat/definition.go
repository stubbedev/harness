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
	var params tools.DefinitionParams
	return renderStandardTool(sty, width, opts, ToolDisplayName(opts.ToolCall), func() ([]string, bool) {
		_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)
		return []string{params.Symbol}, true
	}, func() string {
		// Render code with syntax highlighting when metadata has it.
		var meta tools.DefinitionResponseMetadata
		if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err == nil && meta.Content != "" {
			return toolOutputCodeContent(sty, meta.FilePath, meta.Content, 0, width, opts.ExpandedContent)
		}
		return toolPlainBody(sty, opts, width)
	})
}
