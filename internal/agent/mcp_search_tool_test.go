package agent

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/csync"
)

// searchToolWithRegistry wires a search tool over a server whose registry
// entry holds the given tools for the duration of the test.
func searchToolWithRegistry(t *testing.T, server string, tools []*mcp.Tool) *mcpSearchTool {
	t.Helper()
	t.Cleanup(mcp.RegisterToolsForTest(server, tools))
	return &mcpSearchTool{
		server: server,
		coord:  &coordinator{expandedMCPTools: csync.NewMap[string, map[string]bool]()},
	}
}

func TestMCPSearchToolRun(t *testing.T) {
	tools := []*mcp.Tool{
		{Name: "issue_create", Description: "Open a new issue"},
		{Name: "issue_comment", Description: "Comment on an issue"},
		{Name: "pr_merge", Description: "Merge a pull request"},
		{Name: "release_publish", Description: "Publish a release from an issue milestone"},
	}

	t.Run("query lists matches with the server total", func(t *testing.T) {
		tool := searchToolWithRegistry(t, "forge", tools)

		resp, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"query":"issue"}`})
		require.NoError(t, err)
		require.False(t, resp.IsError)
		assert.Contains(t, resp.Content, "3 tool(s) matching \"issue\" (of 4 total)")
		assert.Contains(t, resp.Content, "- issue_create: Open a new issue")
		assert.NotContains(t, resp.Content, "pr_merge")
	})

	t.Run("name matches rank above description matches", func(t *testing.T) {
		tool := searchToolWithRegistry(t, "forge", tools)

		matches, total := tool.search("issue")
		assert.Equal(t, 3, total)
		// issue_create before issue_comment: equal name matches, but the
		// shorter name wins fzf's unmatched-char penalty.
		assert.Equal(t,
			[]string{"issue_create", "issue_comment", "release_publish"},
			matches)
	})

	// A model writes natural queries, not bare substrings; every term must
	// match somewhere, fuzzily and case-insensitively.
	t.Run("multi-word and fuzzy queries match", func(t *testing.T) {
		tool := searchToolWithRegistry(t, "forge", []*mcp.Tool{
			{Name: "issue_create", Description: "Open a new issue"},
			{Name: "pull_request_merge", Description: "Merge a pull request"},
			{Name: "list_workflow_runs", Description: "List CI workflow runs"},
		})

		tests := []struct {
			query   string
			want    []string
			partial bool
		}{
			{query: "issue", want: []string{"issue_create"}},
			{query: "create issue", want: []string{"issue_create"}},
			{query: "create an issue", want: []string{"issue_create"}},
			{query: "isue", want: []string{"issue_create"}, partial: true},
			{query: "merge PR", want: []string{"pull_request_merge"}},
			{query: "ci runs", want: []string{"list_workflow_runs"}},
			{query: "workflow", want: []string{"list_workflow_runs"}},
		}
		for _, tt := range tests {
			t.Run(tt.query, func(t *testing.T) {
				matches, _ := tool.search(tt.query)
				if tt.partial {
					assert.Contains(t, matches, tt.want[0])
					return
				}
				assert.Equal(t, tt.want, matches)
			})
		}
	})

	t.Run("a query matching nothing says so rather than erroring", func(t *testing.T) {
		tool := searchToolWithRegistry(t, "forge", tools)

		resp, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"query":"kubernetes"}`})
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Contains(t, resp.Content, `No tools matching "kubernetes"`)
	})

	// The cap is what keeps a deferred server's context footprint small,
	// but a truncated list must not read as the whole set.
	t.Run("results are capped", func(t *testing.T) {
		many := make([]*mcp.Tool, 0, mcpSearchResultLimit+5)
		for i := range cap(many) {
			many = append(many, &mcp.Tool{Name: "tool_" + string(rune('a'+i)), Description: "does a thing"})
		}
		tool := searchToolWithRegistry(t, "big", many)

		matches, total := tool.search("tool_")
		assert.Len(t, matches, mcpSearchResultLimit)
		assert.Equal(t, mcpSearchResultLimit+5, total)

		resp, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"query":"tool_"}`})
		require.NoError(t, err)
		require.False(t, resp.IsError)
		assert.Contains(t, resp.Content,
			fmt.Sprintf("%d tool(s) match \"tool_\" (of %d on the server); showing the best %d. Narrow the query to see the others:",
				mcpSearchResultLimit+5, mcpSearchResultLimit+5, mcpSearchResultLimit))
	})

	t.Run("long descriptions are elided", func(t *testing.T) {
		tool := searchToolWithRegistry(t, "verbose", []*mcp.Tool{
			{Name: "chatty", Description: strings.Repeat("x", 500)},
		})

		resp, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"query":"chatty"}`})
		require.NoError(t, err)
		assert.Contains(t, resp.Content, strings.Repeat("x", 200)+"…")
		assert.NotContains(t, resp.Content, strings.Repeat("x", 201))
	})

	t.Run("unknown load names are rejected without expanding anything", func(t *testing.T) {
		tool := searchToolWithRegistry(t, "forge", tools)

		resp, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"load":["issue_create","nope","also_nope"]}`})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, `unknown tool(s) on server "forge": nope, also_nope`)
		assert.False(t, tool.coord.mcpToolExpanded("forge", "issue_create"))
	})

	t.Run("rejections", func(t *testing.T) {
		tests := []struct {
			name  string
			input string
			wants string
		}{
			{"malformed input", `{"query":`, "invalid parameters"},
			{"neither query nor load", `{}`, `provide "query" to search or "load" to load tools`},
			{"empty query and empty load", `{"query":"","load":[]}`, `provide "query" to search or "load" to load tools`},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				tool := searchToolWithRegistry(t, "forge", tools)

				resp, err := tool.Run(t.Context(), fantasy.ToolCall{Input: tt.input})
				require.NoError(t, err)
				assert.True(t, resp.IsError)
				assert.Contains(t, resp.Content, tt.wants)
			})
		}
	})
}

func TestExpandMCPServerTools(t *testing.T) {
	t.Run("unknown names are ignored and known ones stick", func(t *testing.T) {
		c := &coordinator{expandedMCPTools: csync.NewMap[string, map[string]bool]()}
		t.Cleanup(mcp.RegisterToolsForTest("forge", []*mcp.Tool{{Name: "issue_create"}, {Name: "pr_merge"}}))

		// No known name among them: nothing to add, so no tool rebuild is
		// attempted and the coordinator's nil agent is never touched.
		require.NoError(t, c.expandMCPServerTools(t.Context(), "forge", []string{"nope"}))
		assert.False(t, c.mcpToolExpanded("forge", "nope"))

		// Re-expanding an already expanded tool is also a no-op.
		c.expandedMCPTools.Set("forge", map[string]bool{"issue_create": true})
		require.NoError(t, c.expandMCPServerTools(t.Context(), "forge", []string{"issue_create"}))
		assert.True(t, c.mcpToolExpanded("forge", "issue_create"))
		assert.False(t, c.mcpToolExpanded("forge", "pr_merge"))
	})
}

func TestMCPServerToolNames(t *testing.T) {
	c := &coordinator{}
	t.Cleanup(mcp.RegisterToolsForTest("forge", []*mcp.Tool{
		{Name: "pr_merge", Description: "Merge a pull request"},
		{Name: "issue_create"},
	}))

	assert.Equal(t, []string{"issue_create", "pr_merge"}, c.mcpServerToolNames("forge"))
	assert.Equal(t, 2, c.mcpServerToolCount("forge"))
	assert.Equal(t, "Merge a pull request", c.mcpToolDescription("forge", "pr_merge"))
	assert.Empty(t, c.mcpToolDescription("forge", "missing"))
	assert.Empty(t, c.mcpServerToolNames("unknown-server"))
}
