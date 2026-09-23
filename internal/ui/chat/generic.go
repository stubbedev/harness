package chat

import (
	"strings"

	"github.com/stubbedev/harness/internal/ui/styles"
)

// GenericToolRenderContext renders unknown/generic tool messages.
type GenericToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (g *GenericToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return renderStandardTool(sty, width, opts, humanizedToolName(opts.ToolCall.Name), func() ([]string, bool) {
		return jsonToolParams(opts.ToolCall.Input)
	}, func() string {
		if opts.Result.Data != "" && strings.HasPrefix(opts.Result.MIMEType, "image/") {
			return sty.Tool.Body.Render(toolOutputImageContent(sty, opts.Result.Data, opts.Result.MIMEType))
		}
		return renderToolResultTextContent(sty, opts.Result.Content, toolResultContentWidths{Body: width, Diff: width}, opts.ExpandedContent)
	})
}
