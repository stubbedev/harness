package chat

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func displayNameCall(name, input string) message.ToolCall {
	return message.ToolCall{ID: "t1", Name: name, Input: input}
}

// TestToolDisplayNameTable pins the label table: every path that shows a
// tool's name (full renderer headers, one-liners, the task strip, copy
// headings) reads ToolDisplayName, so the table is what users see.
func TestToolDisplayNameTable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		call message.ToolCall
		want string
	}{
		{displayNameCall("shell", "{}"), "Shell"},
		{displayNameCall("view", "{}"), "View"},
		{displayNameCall("write", "{}"), "Write"},
		{displayNameCall("edit", "{}"), "Edit"},
		{displayNameCall("fetch", "{}"), "Fetch"},
		{displayNameCall("web_search", "{}"), "Search"},
		{displayNameCall("question", "{}"), "Question"},
		{displayNameCall("lsp_diagnostics", "{}"), "Diagnostics"},
		// Unknown tools humanize their wire name.
		{displayNameCall("some_future_tool", "{}"), "Some Future Tool"},
		// MCP calls keep the server -> tool split.
		{displayNameCall("mcp_github_create_issue", "{}"), "Github -> Create Issue"},
	} {
		require.Equal(t, tc.want, ToolDisplayName(tc.call), "name %q", tc.call.Name)
	}
}

// TestToolDisplayNameLSPFollowsAction: the lsp tool folds several actions
// into one wire name; its label must match the renderer the dispatch
// picks, so a rename shows as "Rename Symbol" in one-liners and copy
// headings too, not "Lsp".
func TestToolDisplayNameLSPFollowsAction(t *testing.T) {
	t.Parallel()

	sty := styles.ThemeForProvider("")

	for _, tc := range []struct {
		action string
		want   string
	}{
		{"references", "Find References"},
		{"definition", "Find Definition"},
		{"rename", "Rename Symbol"},
		{"replace_symbol", "Replace Symbol"},
		{"call_hierarchy", "Call Hierarchy"},
		{"symbols", "List Symbols"},
		{"restart", "Restart LSP"},
		{"", "Diagnostics"},
		{"bogus", "Diagnostics"},
	} {
		call := displayNameCall("lsp", `{"action":"`+tc.action+`"}`)
		require.Equal(t, tc.want, ToolDisplayName(call), "action %q", tc.action)

		// The dispatch and the label read the same field: a label always
		// exists for the renderer lspToolRenderer picks.
		item := NewToolMessageItem(&sty, "m1", call, nil, false, "")
		require.Equal(t, tc.want, ToolDisplayName(item.ToolCall()))
	}
}
