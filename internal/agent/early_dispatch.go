package agent

import (
	"encoding/json"

	"charm.land/fantasy"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/message"
)

// readOnlyTools are the tools whose every call only reads: running one
// changes nothing in the workspace, so a call can start before its turn is
// settled and can run again after an interruption without doing anything
// twice. Side effects such a call may have (a file marked as seen, a
// PreToolUse hook, a spilled fetch result) are harmless when repeated or
// when the model never sees the result.
//
// Being Parallel is not enough to be listed: edit, write and shell are
// Parallel too.
var readOnlyTools = map[string]bool{
	tools.ViewToolName:      true,
	tools.FetchToolName:     true,
	tools.WebSearchToolName: true,
	tools.HarnessToolName:   true,
	tools.ResearchToolName:  true,
}

// readOnlyToolCall reports whether call only reads. The lsp tool is
// decided by action: its questions (diagnostics, symbols, definition,
// references, call_hierarchy) read, its rename, replace_symbol and restart
// do not.
func readOnlyToolCall(name, input string) bool {
	if name == tools.LSPToolName {
		return tools.LSPCallReadOnly(input)
	}
	return readOnlyTools[name]
}

// earlyToolDispatch reports whether a call may start before the stream it
// arrived in has ended (fantasy.WithEarlyToolDispatch). fantasy only asks
// about calls to Parallel tools whose arguments are already valid. If the
// step then ends on anything but a tool-calls finish, an early call is
// canceled and its result discarded, exactly like a call that never ran,
// but it may already have run by then, so only read-only calls qualify.
//
// Sub-agent dispatches start early too, so the first agent of a fan-out
// runs while the model is still writing the prompts of the rest instead
// of waiting for the whole message. A dispatch the step then discards is
// cancelled and forgotten (see SubagentDispatchDiscarder). The
// dispatcher's wait, message and cancel forms act on agents already
// running and wait for the end of the stream like any other call.
func earlyToolDispatch(call fantasy.ToolCallContent) bool {
	if call.ToolName == AgentToolName {
		var params AgentDispatchParams
		return json.Unmarshal([]byte(call.Input), &params) == nil &&
			params.validateForm() == nil &&
			params.Kind() == AgentCallDispatch
	}
	return readOnlyToolCall(call.ToolName, call.Input)
}

// interruptedToolResult is what the model reads for a call that never
// returned a result because its turn was interrupted. A call that only
// reads is safe to repeat; any other call may have done some or all of
// its work, and the model has to look before it runs it again.
func interruptedToolResult(call message.ToolCall) string {
	if readOnlyToolCall(call.Name, call.Input) {
		return "Error: the tool call was interrupted before it returned a result. It only reads, so it is safe to call again if the result is still needed."
	}
	return "Error: the tool call was interrupted before it returned a result. It may have done some or all of its work: check its effects (files, processes, state) before calling it again."
}

// isSubagentDispatchTool reports whether a tool's calls start sub-agent
// runs.
func isSubagentDispatchTool(name string) bool {
	return name == AgentToolName || name == tools.ResearchToolName
}
