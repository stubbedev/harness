package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
)

const LSPToolName = "lsp"

//go:embed lsp.md
var lspDescription string

// LSPParams is the union of what the language-server actions take. One
// tool with one schema costs a fraction of eight, and the actions have
// most of their parameters in common anyway: nearly all of them name a
// symbol and a place to look for it.
type LSPParams struct {
	Action      string `json:"action" enum:"diagnostics,symbols,definition,references,call_hierarchy,rename,replace_symbol,restart"`
	Symbol      string `json:"symbol,omitempty" description:"Symbol name, for definition, references, call_hierarchy, rename and replace_symbol"`
	FilePath    string `json:"file_path,omitempty" description:"File to act on, for symbols, replace_symbol, and diagnostics (whole project when omitted)"`
	Path        string `json:"path,omitempty" description:"Directory or file to narrow the symbol search to; defaults to the working directory"`
	Direction   string `json:"direction,omitempty" enum:"incoming,outgoing" description:"call_hierarchy only: who calls this, or what this calls"`
	NewName     string `json:"new_name,omitempty" description:"rename only: the new name"`
	Replacement string `json:"replacement,omitempty" description:"replace_symbol only: the text to write, ignored when mode is delete"`
	Mode        string `json:"mode,omitempty" enum:"replace,add_before,add_after,delete" description:"replace_symbol only; replace is the default"`
	Name        string `json:"name,omitempty" description:"restart only: one client to restart; all of them when omitted"`
}

// lspAction is one action's underlying tool and the parameters it takes,
// so a call can be forwarded with exactly the fields that action reads.
type lspAction struct {
	tool   fantasy.AgentTool
	params func(LSPParams) map[string]any
}

// NewLSPTool folds the language-server actions into one tool. Each action
// forwards to the implementation that used to be its own tool: the model
// sees one schema, the code keeps one behaviour per action.
func NewLSPTool(lspManager *lsp.Manager, files history.Service, filetracker filetracker.Service) fantasy.AgentTool {
	actions := map[string]lspAction{
		"diagnostics": {NewDiagnosticsTool(lspManager), func(p LSPParams) map[string]any {
			return map[string]any{"file_path": p.FilePath}
		}},
		"symbols": {NewSymbolsTool(lspManager), func(p LSPParams) map[string]any {
			return map[string]any{"file_path": p.FilePath}
		}},
		"definition": {NewDefinitionTool(lspManager), func(p LSPParams) map[string]any {
			return map[string]any{"symbol": p.Symbol, "path": p.Path}
		}},
		"references": {NewReferencesTool(lspManager), func(p LSPParams) map[string]any {
			return map[string]any{"symbol": p.Symbol, "path": p.Path}
		}},
		"call_hierarchy": {NewCallHierarchyTool(lspManager), func(p LSPParams) map[string]any {
			return map[string]any{"symbol": p.Symbol, "direction": p.Direction, "path": p.Path}
		}},
		"rename": {NewRenameTool(lspManager, files, filetracker), func(p LSPParams) map[string]any {
			return map[string]any{"symbol": p.Symbol, "new_name": p.NewName, "path": p.Path}
		}},
		// The inner tool calls this "action" too; the outer name would
		// shadow it, so the model sends it as "mode".
		"replace_symbol": {NewReplaceSymbolTool(lspManager, files, filetracker), func(p LSPParams) map[string]any {
			return map[string]any{"symbol": p.Symbol, "file_path": p.FilePath, "replacement": p.Replacement, "action": p.Mode}
		}},
		"restart": {NewLSPRestartTool(lspManager), func(p LSPParams) map[string]any {
			return map[string]any{"name": p.Name}
		}},
	}
	known := slices.Sorted(maps.Keys(actions))

	return fantasy.NewAgentTool(
		LSPToolName,
		lspDescription,
		func(ctx context.Context, params LSPParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			action, ok := actions[params.Action]
			if !ok {
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"unknown action %q. Available: %s", params.Action, strings.Join(known, ", "))), nil
			}
			input, err := json.Marshal(action.params(params))
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("lsp %s: %w", params.Action, err)
			}
			call.Input = string(input)
			call.Name = action.tool.Info().Name
			ctx = context.WithValue(ctx, sourceEvidenceKey{}, filetracker)
			return action.tool.Run(ctx, call)
		},
	)
}
