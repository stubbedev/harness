package chat

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/toolname"
)

// TestEveryBuiltinToolHasASpec keeps the transcript in step with the
// tool palette: a new built-in must either get a spec or be declared
// generic, never fall through by omission.
func TestEveryBuiltinToolHasASpec(t *testing.T) {
	t.Parallel()

	for _, name := range append(toolname.Allowlistable(), toolname.SkillSearch, toolname.ToolSearch, toolname.ExtensionJobs) {
		if name == tools.LSPToolName {
			continue
		}
		_, hasSpec := toolSpecs[name]
		require.True(t, hasSpec || genericTools[name], "%s has no toolSpec and is not listed in genericTools", name)
		require.False(t, hasSpec && genericTools[name], "%s is both specified and generic", name)
	}
}

// TestMCPResourceIsNotAnMCPCall pins the built-in that shares the MCP
// prefix to the generic renderer instead of the MCP one, which could
// not split its name and drew "Invalid tool name".
func TestMCPResourceIsNotAnMCPCall(t *testing.T) {
	t.Parallel()

	call := message.ToolCall{Name: tools.MCPResourceToolName}
	require.IsType(t, &GenericToolRenderContext{}, toolRendererFor(call))
	require.Equal(t, "Mcp Resource", callLabel(call))
}

func TestMCPCallUsesTheRecordedServer(t *testing.T) {
	t.Parallel()

	call := message.ToolCall{Name: "mcp_claude_ai_docs_read_page", MCPServer: "claude_ai_docs"}
	require.IsType(t, &MCPToolRenderContext{}, toolRendererFor(call))
	server, tool, ok := mcpCall(call)
	require.True(t, ok)
	require.Equal(t, humanizedToolName("claude_ai_docs"), server)
	require.Equal(t, humanizedToolName("read_page"), tool)
}

func TestMCPSearchStubIsInternal(t *testing.T) {
	t.Parallel()

	require.True(t, IsInternalContextTool(toolname.MCPSearch("sentry")))
	require.False(t, IsInternalContextTool(toolname.MCP("sentry", "get_issue")))
}
