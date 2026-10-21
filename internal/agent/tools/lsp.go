package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

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
	Path        string `json:"path,omitempty" description:"File or directory, relative to the working directory or absolute. symbols and replace_symbol need a file; diagnostics takes a file or covers the whole project when omitted; definition, references, call_hierarchy and rename search under it, defaulting to the working directory"`
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
func adapt[P any](run func(context.Context, P) (fantasy.ToolResponse, error), pick func(LSPParams) (P, error)) lspActionFunc {
	return func(ctx context.Context, p LSPParams) (fantasy.ToolResponse, error) {
		params, err := pick(p)
		if err != nil {
			return fantasy.NewTextErrorResponse(err.Error()), nil
		}
		return run(ctx, params)
	}
}

// lspActions maps each lsp action to its behaviour.
func lspActions(lspManager *lsp.Manager, files history.Service, filetracker filetracker.Service, reg *presence.Registry, workingDir string) map[LSPAction]lspActionFunc {
	paths := lspPaths{workingDir: workingDir}
	return map[LSPAction]lspActionFunc{
		LSPActionDiagnostics: adapt(diagnosticsAction(lspManager), func(p LSPParams) (diagnosticsParams, error) {
			return diagnosticsParams{File: paths.optionalFile(p.Path)}, nil
		}),
		LSPActionSymbols: adapt(symbolsAction(lspManager), func(p LSPParams) (symbolsParams, error) {
			file, err := paths.file(LSPActionSymbols, p.Path)
			return symbolsParams{File: file}, err
		}),
		LSPActionDefinition: adapt(definitionAction(lspManager), func(p LSPParams) (definitionParams, error) {
			return definitionParams{Symbol: p.Symbol, Root: paths.root(p.Path)}, nil
		}),
		LSPActionReferences: adapt(referencesAction(lspManager), func(p LSPParams) (referencesParams, error) {
			return referencesParams{Symbol: p.Symbol, Root: paths.root(p.Path)}, nil
		}),
		LSPActionCallHierarchy: adapt(callHierarchyAction(lspManager), func(p LSPParams) (callHierarchyParams, error) {
			return callHierarchyParams{Symbol: p.Symbol, Direction: p.Direction, Root: paths.root(p.Path)}, nil
		}),
		LSPActionRename: adapt(renameAction(lspManager, files, filetracker, reg), func(p LSPParams) (renameParams, error) {
			return renameParams{Symbol: p.Symbol, NewName: p.NewName, Root: paths.root(p.Path)}, nil
		}),
		// The action parameter would shadow replace_symbol's own "action",
		// so the model sends that one as "mode".
		LSPActionReplaceSymbol: adapt(replaceSymbolAction(lspManager, files, filetracker, reg, workingDir), func(p LSPParams) (replaceSymbolParams, error) {
			file, err := paths.file(LSPActionReplaceSymbol, p.Path)
			return replaceSymbolParams{Symbol: p.Symbol, File: file, Replacement: p.Replacement, Action: p.Mode}, err
		}),
		LSPActionRestart: adapt(lspRestartAction(lspManager), func(p LSPParams) (LSPRestartParams, error) {
			return LSPRestartParams{Name: p.Name}, nil
		}),
	}
}

// lspMutatingActions are the actions that write files or replace a running
// client. Everything else only asks a server a question, which servers
// answer concurrently, so only these are serialised; see
// [lockLSPMutation].
var lspMutatingActions = map[LSPAction]bool{
	LSPActionRename:        true,
	LSPActionReplaceSymbol: true,
	LSPActionRestart:       true,
}

// LSPCallReadOnly reports whether an lsp call's input names an action that
// only asks a server a question. Input that does not parse, or names no
// known action, is not read-only: the caller cannot tell what it would do.
func LSPCallReadOnly(input string) bool {
	var p struct {
		Action LSPAction `json:"action"`
	}
	if err := json.Unmarshal([]byte(input), &p); err != nil {
		return false
	}
	switch p.Action {
	case LSPActionDiagnostics, LSPActionSymbols, LSPActionDefinition, LSPActionReferences, LSPActionCallHierarchy:
		return true
	case LSPActionRename, LSPActionReplaceSymbol, LSPActionRestart:
		return false
	}
	return false
}

// lspMutationLocks holds one single-slot semaphore per language-server
// manager. It is keyed by the manager rather than held by the tool because
// the tool is rebuilt whenever an agent's tool set is (a tool search load,
// a sub-agent dispatch), and every one of those instances edits the same
// workspace through the same servers.
var lspMutationLocks sync.Map // *lsp.Manager -> chan struct{}

// lockLSPMutation waits for the right to run a mutating lsp action against
// manager and returns its release. Two renames at once would each compute
// their workspace edit from a tree the other is rewriting, and a restart
// would pull the client out from under a rename mid-apply, so mutations
// take turns. Read-only actions never take it. The wait gives up when ctx
// ends, so a cancelled call does not queue behind a slow rename.
func lockLSPMutation(ctx context.Context, manager *lsp.Manager) (func(), error) {
	slot, _ := lspMutationLocks.LoadOrStore(manager, make(chan struct{}, 1))
	sem := slot.(chan struct{})
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// NewLSPTool folds the language-server actions into one tool: the model
// sees one schema, the code keeps one behaviour per action.
func NewLSPTool(lspManager *lsp.Manager, files history.Service, filetracker filetracker.Service, reg *presence.Registry, workingDir string) fantasy.AgentTool {
	return newLSPTool(lspManager, filetracker, lspActions(lspManager, files, filetracker, reg, workingDir))
}

// newLSPTool builds the lsp tool over a given action table, so a test can
// drive the dispatch and its locking with actions of its own.
//
// The tool is parallel: definition, references, symbols, call hierarchy
// and diagnostics are questions a server answers concurrently, and a
// sequential tool holds every other call of its step behind it. The
// actions that change files serialise among themselves instead (see
// [lockLSPMutation]).
func newLSPTool(lspManager *lsp.Manager, filetracker filetracker.Service, actions map[LSPAction]lspActionFunc) fantasy.AgentTool {
	var known []string
	for _, action := range slices.Sorted(maps.Keys(actions)) {
		known = append(known, string(action))
	}

	return fantasy.NewParallelAgentTool(
		LSPToolName,
		lspDescription,
		func(ctx context.Context, params LSPParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			name := LSPAction(params.Action)
			action, ok := actions[name]
			if !ok {
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"unknown action %q. Available: %s", params.Action, strings.Join(known, ", "))), nil
			}
			if lspMutatingActions[name] {
				unlock, err := lockLSPMutation(ctx, lspManager)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf(
						"%s cancelled while waiting for another lsp change to finish: %s", name, err)), nil
				}
				defer unlock()
			}
			ctx = context.WithValue(ctx, sourceEvidenceKey{}, filetracker)
			return action(ctx, params)
		},
	)
}
