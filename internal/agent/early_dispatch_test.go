package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agent/tools"
)

// TestEarlyToolDispatchOnlyReadOnlyTools pins the early-dispatch list to
// tools that read. The mutating tools are Parallel as well, so a careless
// "every Parallel tool" rule would start an edit or a command before the
// model's turn is known to be complete.
func TestEarlyToolDispatchOnlyReadOnlyTools(t *testing.T) {
	t.Parallel()

	for _, name := range []string{tools.ViewToolName, tools.FetchToolName, tools.WebSearchToolName, tools.HarnessToolName} {
		require.True(t, earlyToolDispatch(fantasy.ToolCallContent{ToolName: name}), name)
	}
	for _, name := range []string{
		tools.EditToolName, tools.WriteToolName, tools.ShellToolName,
		AgentToolName, tools.ResearchToolName, tools.MemoryToolName,
		tools.QuestionToolName, tools.MCPResourceToolName,
	} {
		require.False(t, earlyToolDispatch(fantasy.ToolCallContent{ToolName: name}), name)
	}
}

// lsp calls start early only for the actions that ask a server a
// question; the ones that write files or replace a client wait for the
// message to finish.
func TestEarlyToolDispatchLSPByAction(t *testing.T) {
	t.Parallel()

	for _, action := range []tools.LSPAction{
		tools.LSPActionDiagnostics, tools.LSPActionSymbols, tools.LSPActionDefinition,
		tools.LSPActionReferences, tools.LSPActionCallHierarchy,
	} {
		call := fantasy.ToolCallContent{ToolName: tools.LSPToolName, Input: `{"action":"` + string(action) + `"}`}
		require.True(t, earlyToolDispatch(call), action)
	}
	for _, input := range []string{
		`{"action":"rename","symbol":"A","new_name":"B"}`,
		`{"action":"replace_symbol"}`,
		`{"action":"restart"}`,
		`{"action":"unknown"}`,
		`{}`,
		`not json`,
	} {
		require.False(t, earlyToolDispatch(fantasy.ToolCallContent{ToolName: tools.LSPToolName, Input: input}), input)
	}
}
