package toolname

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitMCP(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, server      string
		wantSrv, wantTool string
		ok                bool
	}{
		{"mcp_sentry_get_issue", "sentry", "sentry", "get_issue", true},
		{"mcp_claude_ai_docs_read", "claude_ai_docs", "claude_ai_docs", "read", true},
		{"mcp_sentry_get_issue", "", "sentry", "get_issue", true},
		{"mcp_resource", "", "", "", false},
		{"view", "", "", "", false},
		{"mcp_other_tool", "sentry", "sentry", "", false},
	} {
		srv, tool, ok := SplitMCP(tc.name, tc.server)
		require.Equal(t, tc.ok, ok, tc.name)
		if ok {
			require.Equal(t, tc.wantSrv, srv, tc.name)
			require.Equal(t, tc.wantTool, tool, tc.name)
		}
	}
}

func TestMCPNamesRoundTrip(t *testing.T) {
	t.Parallel()

	srv, tool, ok := SplitMCP(MCP("a_b", "c_d"), "a_b")
	require.True(t, ok)
	require.Equal(t, "a_b", srv)
	require.Equal(t, "c_d", tool)
	require.True(t, IsMCPSearch(MCPSearch("a_b"), "a_b"))
	require.True(t, IsMCPSearch(MCPSearch("a_b"), ""))
	require.False(t, IsMCPSearch(MCP("a_b", "read"), "a_b"))
}

func TestBuiltinsAreNotMCP(t *testing.T) {
	t.Parallel()

	for _, name := range Allowlistable() {
		_, _, ok := SplitMCP(name, "")
		require.False(t, ok, name)
		require.True(t, IsBuiltin(name), name)
	}
}
