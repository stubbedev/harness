package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/csync"
)

type noArgs struct{}

func namedTool(name, description string) fantasy.AgentTool {
	return fantasy.NewAgentTool(name, description, func(context.Context, noArgs, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
}

func toolNames(list []fantasy.AgentTool) []string {
	names := make([]string, 0, len(list))
	for _, t := range list {
		names = append(names, t.Info().Name)
	}
	return names
}

func findTool(t *testing.T, list []fantasy.AgentTool, name string) fantasy.AgentTool {
	t.Helper()
	for _, tool := range list {
		if tool.Info().Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not in %v", name, toolNames(list))
	return nil
}

// The long tail of built-in tools is absent from the top-level agent's
// initial tool set and stands behind tool_search, whose description names
// each hidden tool; a sub-agent gets everything inline.
func TestDeferredBuiltinToolsHideBehindToolSearch(t *testing.T) {
	t.Parallel()
	c := &coordinator{expandedBuiltins: csync.NewMap[string, bool]()}
	built := []fantasy.AgentTool{
		namedTool(tools.ShellToolName, "Run commands."),
		namedTool(tools.LSPToolName, "Ask the language server about code by symbol name rather than by text. Set action."),
		namedTool(tools.HarnessToolName, "Inspect Harness itself."),
		namedTool(tools.MemoryToolName, "Save memories."),
	}

	top := c.deferBuiltinTools(built, false)
	assert.ElementsMatch(t, []string{tools.ShellToolName, tools.MemoryToolName, ToolSearchToolName}, toolNames(top),
		"lsp and harness are deferred; shell and memory stay inline")
	search := findTool(t, top, ToolSearchToolName).Info()
	assert.Contains(t, search.Description, "harness, lsp")
	assert.Contains(t, search.Description, "- lsp: Ask the language server about code by symbol name rather than by text")
	assert.NotContains(t, search.Description, "Set action", "one sentence per tool")

	sub := c.deferBuiltinTools(built, true)
	assert.ElementsMatch(t, toolNames(built), toolNames(sub), "sub-agents get every tool inline")
}

// A tool the config removed never reaches the deferral, so it is neither
// inline nor offered by tool_search.
func TestDeferredBuiltinToolsRespectTheAllowList(t *testing.T) {
	t.Parallel()
	c := &coordinator{expandedBuiltins: csync.NewMap[string, bool]()}
	// The allow-list filter already dropped lsp.
	built := []fantasy.AgentTool{
		namedTool(tools.ShellToolName, "Run commands."),
		namedTool(tools.HarnessToolName, "Inspect Harness itself."),
	}
	top := c.deferBuiltinTools(built, false)
	search := findTool(t, top, ToolSearchToolName).Info()
	assert.NotContains(t, search.Description, "lsp")

	resp, err := findTool(t, top, ToolSearchToolName).Run(t.Context(), fantasy.ToolCall{ID: "x", Name: ToolSearchToolName, Input: `{"load":["lsp"]}`})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "a tool that is not offered cannot be loaded")
	assert.Contains(t, resp.Content, "unknown tool(s): lsp")
}

// Loading through tool_search puts the tool in the next build and keeps
// it there; once nothing is left to load the search tool itself goes.
func TestToolSearchLoadsForTheSession(t *testing.T) {
	t.Parallel()
	c := &coordinator{expandedBuiltins: csync.NewMap[string, bool]()}
	built := []fantasy.AgentTool{
		namedTool(tools.LSPToolName, "Ask the language server."),
		namedTool(tools.HarnessToolName, "Inspect Harness itself."),
	}

	first := c.deferBuiltinTools(built, false)
	assert.ElementsMatch(t, []string{ToolSearchToolName}, toolNames(first))

	resp, err := findTool(t, first, ToolSearchToolName).Run(t.Context(), fantasy.ToolCall{ID: "x", Name: ToolSearchToolName, Input: `{"load":["lsp"]}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Loaded 1 tool(s): lsp")

	second := c.deferBuiltinTools(built, false)
	assert.ElementsMatch(t, []string{tools.LSPToolName, ToolSearchToolName}, toolNames(second),
		"lsp is inline from the next build; harness still waits behind the search")
	assert.NotContains(t, findTool(t, second, ToolSearchToolName).Info().Description, "- lsp:")

	_, err = findTool(t, second, ToolSearchToolName).Run(t.Context(), fantasy.ToolCall{ID: "y", Name: ToolSearchToolName, Input: `{"load":["harness"]}`})
	require.NoError(t, err)
	third := c.deferBuiltinTools(built, false)
	assert.ElementsMatch(t, []string{tools.LSPToolName, tools.HarnessToolName}, toolNames(third),
		"with everything loaded there is nothing to search for")
}
