package chat

import (
	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/toolname"
)

// toolSpec is everything the transcript knows about one built-in tool:
// how its call renders, what it is called and how it copies. Every
// surface reads the same entry, so a tool cannot render under one name
// and copy under another. TestEveryBuiltinToolHasASpec holds each
// built-in to having an entry or being listed in genericTools.
type toolSpec struct {
	// label is the call's header name. Empty falls back to the
	// humanized wire name.
	label string
	// renderer builds the call's body renderer. Nil renders generically.
	renderer func() ToolRenderer
	// copyParams formats the call's input for the clipboard. Nil, or an
	// empty result, copies the raw input as JSON.
	copyParams func(input string) string
	// copyResult formats the call's result for the clipboard. Nil
	// fences the result as opaque text.
	copyResult func(*baseToolMessageItem) string
}

// toolSpecs is keyed by wire name. The lsp tool is absent on purpose: it
// folds several actions into one name and has its own item, which
// [lspDisplayName] and [lspToolRenderer] dispatch by action.
var toolSpecs = map[string]toolSpec{
	tools.ShellToolName: {
		label:      "Shell",
		renderer:   func() ToolRenderer { return &ShellToolRenderContext{} },
		copyParams: copyShellParams,
		copyResult: (*baseToolMessageItem).formatShellResultForCopy,
	},
	tools.ViewToolName: {
		label:      "View",
		renderer:   func() ToolRenderer { return &ViewToolRenderContext{} },
		copyParams: copyViewParams,
		copyResult: (*baseToolMessageItem).formatViewResultForCopy,
	},
	tools.WriteToolName: {
		label:      "Write",
		renderer:   func() ToolRenderer { return &WriteToolRenderContext{} },
		copyParams: copyWriteParams,
		copyResult: (*baseToolMessageItem).formatWriteResultForCopy,
	},
	tools.EditToolName: {
		label:      "Edit",
		renderer:   func() ToolRenderer { return &EditToolRenderContext{} },
		copyParams: copyEditParams,
		copyResult: (*baseToolMessageItem).formatEditResultForCopy,
	},
	tools.FetchToolName: {
		label:      "Fetch",
		renderer:   func() ToolRenderer { return &FetchToolRenderContext{} },
		copyParams: copyFetchParams,
		copyResult: (*baseToolMessageItem).formatFetchResultForCopy,
	},
	tools.WebSearchToolName: {
		label:    "Search",
		renderer: func() ToolRenderer { return &WebSearchToolRenderContext{} },
	},
	tools.QuestionToolName: {
		label:    "Question",
		renderer: func() ToolRenderer { return &QuestionToolRenderContext{} },
	},
	tools.ResearchToolName: {
		copyParams: copyResearchParams,
		copyResult: copyMarkdownResult,
	},
	agent.AgentToolName: {
		copyParams: copyAgentParams,
		copyResult: copyMarkdownResult,
	},
	// lsp_diagnostics is no longer registered; sessions recorded before
	// it folded into the lsp tool still hold calls to it.
	tools.DiagnosticsToolName: {
		label:      "Diagnostics",
		copyParams: copyDiagnosticsParams,
		copyResult: func(t *baseToolMessageItem) string { return copyFence("", t.result.Content) },
	},
}

// genericTools are the built-ins that render, label and copy generically
// by design.
var genericTools = map[string]bool{
	toolname.Harness:       true,
	toolname.Memory:        true,
	toolname.Verify:        true,
	toolname.MCPResource:   true,
	toolname.SendMessage:   true,
	toolname.SkillSearch:   true,
	toolname.ToolSearch:    true,
	toolname.ExtensionJobs: true,
}

func copyMarkdownResult(t *baseToolMessageItem) string {
	return copyFence("markdown", t.result.Content)
}

// mcpCall reports the humanized server and tool of an MCP call. The
// recorded server decides the split exactly; calls stored before it was
// recorded fall back to the first underscore. Built-in tools that share
// the prefix, such as mcp_resource, are never MCP calls.
func mcpCall(tc message.ToolCall) (server, tool string, ok bool) {
	server, tool, ok = toolname.SplitMCP(tc.Name, tc.MCPServer)
	if !ok {
		return "", "", false
	}
	return humanizedToolName(server), humanizedToolName(tool), true
}
