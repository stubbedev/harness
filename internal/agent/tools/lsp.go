package tools

import (
	"context"
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/stubbedev/harness/internal/presence"

	"github.com/stubbedev/harness/internal/toolname"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
)

const LSPToolName = toolname.LSP

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

// LSPAction names one of the actions the lsp tool dispatches. The UI labels
// and renders an lsp call by it, so both sides spell it one way.
type LSPAction string

// The lsp tool's actions. The enum tag on LSPParams.Action must list these;
// TestLSPActionEnumMatchesDispatch holds it to that.
const (
	LSPActionDiagnostics   LSPAction = "diagnostics"
	LSPActionSymbols       LSPAction = "symbols"
	LSPActionDefinition    LSPAction = "definition"
	LSPActionReferences    LSPAction = "references"
	LSPActionCallHierarchy LSPAction = "call_hierarchy"
	LSPActionRename        LSPAction = "rename"
	LSPActionReplaceSymbol LSPAction = "replace_symbol"
	LSPActionRestart       LSPAction = "restart"
)

// lspActionFunc runs one language-server action with the fields of the
// combined parameters that action reads.
type lspActionFunc func(context.Context, LSPParams) (fantasy.ToolResponse, error)

// adapt turns an action that takes its own parameter type into an
// lspActionFunc, picking its fields out of the combined parameters.
func adapt[P any](run func(context.Context, P) (fantasy.ToolResponse, error), pick func(LSPParams) P) lspActionFunc {
	return func(ctx context.Context, p LSPParams) (fantasy.ToolResponse, error) {
		return run(ctx, pick(p))
	}
}

// lspActions maps each lsp action to its behaviour.
func lspActions(lspManager *lsp.Manager, files history.Service, filetracker filetracker.Service, reg *presence.Registry) map[LSPAction]lspActionFunc {
	return map[LSPAction]lspActionFunc{
		LSPActionDiagnostics: adapt(diagnosticsAction(lspManager), func(p LSPParams) DiagnosticsParams {
			return DiagnosticsParams{FilePath: p.FilePath}
		}),
		LSPActionSymbols: adapt(symbolsAction(lspManager), func(p LSPParams) SymbolsParams {
			return SymbolsParams{FilePath: p.FilePath}
		}),
		LSPActionDefinition: adapt(definitionAction(lspManager), func(p LSPParams) DefinitionParams {
			return DefinitionParams{Symbol: p.Symbol, Path: p.Path}
		}),
		LSPActionReferences: adapt(referencesAction(lspManager), func(p LSPParams) ReferencesParams {
			return ReferencesParams{Symbol: p.Symbol, Path: p.Path}
		}),
		LSPActionCallHierarchy: adapt(callHierarchyAction(lspManager), func(p LSPParams) CallHierarchyParams {
			return CallHierarchyParams{Symbol: p.Symbol, Direction: p.Direction, Path: p.Path}
		}),
		LSPActionRename: adapt(renameAction(lspManager, files, filetracker, reg), func(p LSPParams) RenameParams {
			return RenameParams{Symbol: p.Symbol, NewName: p.NewName, Path: p.Path}
		}),
		// The action parameter would shadow replace_symbol's own "action",
		// so the model sends that one as "mode".
		LSPActionReplaceSymbol: adapt(replaceSymbolAction(lspManager, files, filetracker, reg), func(p LSPParams) ReplaceSymbolParams {
			return ReplaceSymbolParams{Symbol: p.Symbol, FilePath: p.FilePath, Replacement: p.Replacement, Action: p.Mode}
		}),
		LSPActionRestart: adapt(lspRestartAction(lspManager), func(p LSPParams) LSPRestartParams {
			return LSPRestartParams{Name: p.Name}
		}),
	}
}

// NewLSPTool folds the language-server actions into one tool: the model
// sees one schema, the code keeps one behaviour per action.
func NewLSPTool(lspManager *lsp.Manager, files history.Service, filetracker filetracker.Service, reg *presence.Registry) fantasy.AgentTool {
	actions := lspActions(lspManager, files, filetracker, reg)
	var known []string
	for _, action := range slices.Sorted(maps.Keys(actions)) {
		known = append(known, string(action))
	}

	return fantasy.NewAgentTool(
		LSPToolName,
		lspDescription,
		func(ctx context.Context, params LSPParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			action, ok := actions[LSPAction(params.Action)]
			if !ok {
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"unknown action %q. Available: %s", params.Action, strings.Join(known, ", "))), nil
			}
			ctx = context.WithValue(ctx, sourceEvidenceKey{}, filetracker)
			return action(ctx, params)
		},
	)
}
