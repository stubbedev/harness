package chat

import (
	"fmt"

	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// MCPToolRenderContext renders MCP tool messages.
type MCPToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (b *MCPToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	server, tool, ok := splitMCPName(opts.ToolCall.Name)
	if !ok {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid tool name"}, width)
	}
	name := fmt.Sprintf("%s %s %s", sty.Tool.MCPName.Render(server), sty.Tool.MCPArrow.String(), sty.Tool.MCPToolName.Render(tool))
	return renderStandardTool(sty, width, opts, name, func() ([]string, bool) {
		return jsonToolParams(opts.ToolCall.Input)
	}, func() string {
		return renderToolResultTextContent(sty, opts.Result.Content, toolResultContentWidths{Body: width, Diff: width}, opts.ExpandedContent)
	})
}
