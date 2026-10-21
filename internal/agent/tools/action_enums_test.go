package tools

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/memory"
)

func schemaEnum(t *testing.T, tool fantasy.AgentTool, field string) any {
	t.Helper()
	property, ok := tool.Info().Parameters[field].(map[string]any)
	require.True(t, ok, field)
	return property["enum"]
}

func TestMultiActionToolsAdvertiseTheirDispatch(t *testing.T) {
	t.Parallel()

	store, err := memory.OpenStore(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	lspTool := NewLSPTool(nil, nil, nil, nil, "")
	cases := map[string]struct {
		tool  fantasy.AgentTool
		field string
		want  []string
	}{
		"harness":      {NewHarnessTool(nil, nil, nil, nil, nil, ""), "action", []string{"logs", "state"}},
		"memory":       {NewMemoryTool(memory.NewService(store, "example.com/team/tool")), "action", []string{"delete", "edit", "list", "read", "save", "search"}},
		"mcp_resource": {NewMCPResourceTool(nil), "action", []string{"list", "read"}},
		"lsp action":   {lspTool, "action", []string{"call_hierarchy", "definition", "diagnostics", "references", "rename", "replace_symbol", "restart", "symbols"}},
		"lsp mode":     {lspTool, "mode", []string{"add_after", "add_before", "delete", "replace"}},
	}
	for name, tc := range cases {
		require.Equal(t, tc.want, schemaEnum(t, tc.tool, tc.field), name)
	}
}

func TestMultiActionToolsRejectUnknownActionsByTheirTable(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		tool  fantasy.AgentTool
		input string
		want  string
	}{
		"harness":      {NewHarnessTool(nil, nil, nil, nil, nil, ""), `{"action":"flurb"}`, `unknown action "flurb"; available: logs, state`},
		"mcp_resource": {NewMCPResourceTool(nil), `{"action":"flurb","mcp_name":"m"}`, `unknown action "flurb"; available: list, read`},
		"lsp":          {NewLSPTool(nil, nil, nil, nil, ""), `{"action":"flurb"}`, `unknown action "flurb"; available: call_hierarchy, definition, diagnostics, references, rename, replace_symbol, restart, symbols`},
	}
	for name, tc := range cases {
		resp, err := tc.tool.Run(t.Context(), fantasy.ToolCall{Input: tc.input})
		require.NoError(t, err, name)
		require.True(t, resp.IsError, name)
		require.Equal(t, tc.want, resp.Content, name)
	}
}

func TestReplaceSymbolRejectsUnknownMode(t *testing.T) {
	t.Parallel()

	resp, err := replaceSymbolAction(nil, nil, nil, nil, "")(t.Context(), replaceSymbolParams{Symbol: "f", Action: "flurb"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Equal(t, `unknown mode "flurb"; available: add_after, add_before, delete, replace`, resp.Content)
}
