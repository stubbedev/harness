package agent

import (
	"charm.land/fantasy"

	"github.com/stubbedev/harness/internal/agent/tools"
)

// earlyDispatchTools are the tools whose calls may start while the model is
// still streaming the rest of its message (fantasy.WithEarlyToolDispatch).
//
// Being Parallel is not enough to be listed: edit, write, shell and the
// sub-agent tools are Parallel too. If the step then ends on anything but a
// tool-calls finish, an early call is canceled and its result discarded,
// exactly like a call that never ran, but it may already have run by then.
// So only tools that read, and whose side effects (a file marked as seen, a
// PreToolUse hook, a spilled fetch result) are harmless for a call whose
// result the model never saw, belong here.
var earlyDispatchTools = map[string]bool{
	tools.ViewToolName:      true,
	tools.FetchToolName:     true,
	tools.WebSearchToolName: true,
	tools.HarnessToolName:   true,
}

// earlyToolDispatch reports whether a call may start before the stream it
// arrived in has ended. fantasy only asks about calls to Parallel tools
// whose arguments are already valid.
//
// The lsp tool is listed by action: its questions (diagnostics, symbols,
// definition, references, call_hierarchy) qualify, its rename,
// replace_symbol and restart do not.
func earlyToolDispatch(call fantasy.ToolCallContent) bool {
	if call.ToolName == tools.LSPToolName {
		return tools.LSPCallReadOnly(call.Input)
	}
	return earlyDispatchTools[call.ToolName]
}
