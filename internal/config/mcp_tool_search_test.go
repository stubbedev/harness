package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMCPConfigDeferToolSearch pins the default: every MCP server's tool
// schemas stay out of the model's context behind the search tool unless
// the server explicitly asks to be loaded eagerly.
func TestMCPConfigDeferToolSearch(t *testing.T) {
	t.Parallel()

	boolPtr := func(b bool) *bool { return &b }

	require.True(t, MCPConfig{}.DeferToolSearch(), "deferring is the default")
	require.True(t, MCPConfig{ToolSearch: boolPtr(true)}.DeferToolSearch())
	require.False(t, MCPConfig{ToolSearch: boolPtr(false)}.DeferToolSearch(),
		"tool_search: false opts a server back into eager loading")
}
