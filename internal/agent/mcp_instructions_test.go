package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/hooks"
)

// fakeMCPTool is a tool that reports a server, the way the real MCP tool
// wrapper does.
type fakeMCPTool struct {
	fakeTool
	server string
}

func newFakeMCPTool(name, server string) *fakeMCPTool {
	tool := &fakeMCPTool{server: server}
	tool.name = name
	return tool
}

func (f *fakeMCPTool) MCP() string { return f.server }

func TestLiveMCPServers(t *testing.T) {
	t.Parallel()

	tools := []fantasy.AgentTool{
		&fakeTool{name: "shell"},
		newFakeMCPTool("mcp_sentry_get_issue", "sentry"),
		&mcpSearchTool{server: "jenkins"},
	}

	live := liveMCPServers(tools)
	require.True(t, live["sentry"], "a server with one of its own tools is live")
	require.False(t, live["jenkins"], "a server standing behind its search stub is not live")
	require.False(t, live["shell"], "a non-MCP tool contributes no server")
}

// TestLiveMCPServersSeesThroughHooks covers the reason hookedTool forwards
// MCP(): with any tool hook configured every tool in the list is wrapped, and
// a wrapper that swallowed the server name would make every server look
// deferred and silently drop every server's instructions.
func TestLiveMCPServersSeesThroughHooks(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: "exit 0"}},
	})
	wrapped := wrapToolsWithHooks([]fantasy.AgentTool{
		newFakeMCPTool("mcp_sentry_get_issue", "sentry"),
	}, registry)
	require.IsType(t, &hookedTool{}, wrapped[0], "the hook registry must actually wrap")

	require.True(t, liveMCPServers(wrapped)["sentry"])
}

func TestHookedToolMCPIsEmptyForPlainTools(t *testing.T) {
	t.Parallel()

	tool := newHookedTool(&fakeTool{name: "shell"}, nil)
	require.Empty(t, tool.MCP())
}
