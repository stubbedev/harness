package tools

import (
	"context"
	_ "embed"
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

// NewLSPTool folds the language-server actions into one tool: the model
// sees one schema, the code keeps one behaviour per action.
func NewLSPTool(lspManager *lsp.Manager, files history.Service, filetracker filetracker.Service) fantasy.AgentTool {
	actions := map[string]lspActionFunc{
		"diagnostics": adapt(diagnosticsAction(lspManager), func(p LSPParams) DiagnosticsParams {
			return DiagnosticsParams{FilePath: p.FilePath}
		}),
		"symbols": adapt(symbolsAction(lspManager), func(p LSPParams) SymbolsParams {
			return SymbolsParams{FilePath: p.FilePath}
		}),
		"definition": adapt(definitionAction(lspManager), func(p LSPParams) DefinitionParams {
			return DefinitionParams{Symbol: p.Symbol, Path: p.Path}
		}),
		"references": adapt(referencesAction(lspManager), func(p LSPParams) ReferencesParams {
			return ReferencesParams{Symbol: p.Symbol, Path: p.Path}
		}),
		"call_hierarchy": adapt(callHierarchyAction(lspManager), func(p LSPParams) CallHierarchyParams {
			return CallHierarchyParams{Symbol: p.Symbol, Direction: p.Direction, Path: p.Path}
		}),
		"rename": adapt(renameAction(lspManager, files, filetracker), func(p LSPParams) RenameParams {
			return RenameParams{Symbol: p.Symbol, NewName: p.NewName, Path: p.Path}
		}),
		// The action parameter would shadow replace_symbol's own "action",
		// so the model sends that one as "mode".
		"replace_symbol": adapt(replaceSymbolAction(lspManager, files, filetracker), func(p LSPParams) ReplaceSymbolParams {
			return ReplaceSymbolParams{Symbol: p.Symbol, FilePath: p.FilePath, Replacement: p.Replacement, Action: p.Mode}
		}),
		"restart": adapt(lspRestartAction(lspManager), func(p LSPParams) LSPRestartParams {
			return LSPRestartParams{Name: p.Name}
		}),
	}
	known := slices.Sorted(maps.Keys(actions))

	return fantasy.NewAgentTool(
		LSPToolName,
		lspDescription,
		func(ctx context.Context, params LSPParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			action, ok := actions[params.Action]
			if !ok {
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"unknown action %q. Available: %s", params.Action, strings.Join(known, ", "))), nil
			}
			ctx = context.WithValue(ctx, sourceEvidenceKey{}, filetracker)
			return action(ctx, params)
		},
	)
}
