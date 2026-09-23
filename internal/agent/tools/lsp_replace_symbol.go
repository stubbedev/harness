package tools

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
)

type ReplaceSymbolParams struct {
	Symbol      string `json:"symbol" description:"The symbol name to target (e.g., function name, method name, type name)"`
	FilePath    string `json:"file_path" description:"The path to the file containing the symbol"`
	Replacement string `json:"replacement,omitempty" description:"The replacement text. Required for 'replace' action. For 'add_before'/'add_after', the text to insert. Ignored for 'delete'."`
	Action      string `json:"action,omitempty" description:"Operation to perform: 'replace' (default, replace entire symbol), 'add_before' (insert before symbol), 'add_after' (insert after symbol), 'delete' (remove symbol entirely)"`
}

const ReplaceSymbolToolName = "lsp_replace_symbol"

//go:embed lsp_replace_symbol.md
var replaceSymbolDescription string

// ReplaceSymbolResponseMetadata carries diff data for the renderer.
type ReplaceSymbolResponseMetadata struct {
	FilePath   string `json:"file_path"`
	OldContent string `json:"old_content"`
	NewContent string `json:"new_content"`
	Action     string `json:"action"`
}

// replaceSymbolOp is what one replace_symbol action does to the lines of
// a symbol spanning start..end (0-based, inclusive): the half-open range
// it cuts, whether it inserts the replacement there, and how it reports
// itself (with 1-based lines).
type replaceSymbolOp struct {
	span    func(start, end int) (from, to int)
	insert  bool
	summary func(symbol, path string, start, end int) string
}

var replaceSymbolOps = map[string]replaceSymbolOp{
	"replace": {
		span:   func(start, end int) (int, int) { return start, end + 1 },
		insert: true,
		summary: func(symbol, path string, start, end int) string {
			return fmt.Sprintf("Replaced symbol '%s' in %s (lines %d-%d)", symbol, path, start, end)
		},
	},
	"add_before": {
		span:   func(start, _ int) (int, int) { return start, start },
		insert: true,
		summary: func(symbol, path string, start, _ int) string {
			return fmt.Sprintf("Inserted before symbol '%s' in %s (before line %d)", symbol, path, start)
		},
	},
	"add_after": {
		span:   func(_, end int) (int, int) { return end + 1, end + 1 },
		insert: true,
		summary: func(symbol, path string, _, end int) string {
			return fmt.Sprintf("Inserted after symbol '%s' in %s (after line %d)", symbol, path, end)
		},
	},
	"delete": {
		span: func(start, end int) (int, int) { return start, end + 1 },
		summary: func(symbol, path string, start, end int) string {
			return fmt.Sprintf("Deleted symbol '%s' from %s (lines %d-%d)", symbol, path, start, end)
		},
	},
}

func NewReplaceSymbolTool(
	lspManager *lsp.Manager,
	files history.Service,
	tracker filetracker.Service,
) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ReplaceSymbolToolName,
		replaceSymbolDescription,
		func(ctx context.Context, params ReplaceSymbolParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Symbol == "" {
				return fantasy.NewTextErrorResponse("symbol is required"), nil
			}
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			action := params.Action
			if action == "" {
				action = "replace"
			}
			op, ok := replaceSymbolOps[action]
			if !ok {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("invalid action %q: must be replace, add_before, add_after, or delete", action)), nil
			}
			if op.insert && params.Replacement == "" {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("replacement is required for action %q", action)), nil
			}

			lspManager.Start(ctx, params.FilePath)

			client := findLSPClient(lspManager, params.FilePath)
			if client == nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("no LSP client handles file: %s", params.FilePath)), nil
			}

			symbols, err := client.DocumentSymbols(ctx, params.FilePath)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to get document symbols: %s", err)), nil
			}

			target := findSymbolByName(symbols, params.Symbol)
			if target == nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("symbol '%s' not found in %s", params.Symbol, params.FilePath)), nil
			}

			rng := target.GetRange()

			unlock := lockFile(params.FilePath)
			defer unlock()
			content, err := os.ReadFile(params.FilePath)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("failed to read file: %w", err)
			}

			lines := strings.Split(string(content), "\n")
			startLine := int(rng.Start.Line)
			endLine := int(rng.End.Line)
			if startLine >= len(lines) || endLine >= len(lines) {
				return fantasy.NewTextErrorResponse("symbol range exceeds file length"), nil
			}

			// Cut lines [from, to) and put the replacement there when the
			// action inserts one.
			from, to := op.span(startLine, endLine)
			var inserted []string
			if op.insert {
				inserted = strings.Split(params.Replacement, "\n")
			}
			newContent := strings.Join(slices.Concat(lines[:from], inserted, lines[to:]), "\n")

			sessionID := GetSessionFromContext(ctx)
			affected := lineRange(content, startLine, endLine-startLine+1)
			if err := checkFileEvidence(ctx, tracker, sessionID, params.FilePath, content, []filetracker.Range{affected}); err != nil {
				return fantasy.NewTextErrorResponse(conflictEvidence(ctx, tracker, sessionID, params.FilePath, content, affected.Start, err).Error()), nil
			}
			if err := guardedWrite(params.FilePath, content, []byte(newContent), false); err != nil {
				if current, readErr := os.ReadFile(params.FilePath); readErr == nil {
					err = conflictEvidence(ctx, tracker, sessionID, params.FilePath, current, affected.Start, err)
				}
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if files != nil && sessionID != "" {
				if err := recordFileVersion(ctx, files, sessionID, params.FilePath, string(content), newContent); err != nil {
					slog.Warn("Failed to record file history for replace", "path", params.FilePath, "error", err)
				}
			}

			filetracker.Advance(ctx, tracker, sessionID, params.FilePath, content, []byte(newContent))

			lspManager.NotifyChangeAsync(ctx, params.FilePath)

			summary := op.summary(params.Symbol, params.FilePath, startLine+1, endLine+1)

			resp := fantasy.NewTextResponse(summary + "\n" + reportDiagnosticsNow(ctx, lspManager, params.FilePath))
			resp = fantasy.WithResponseMetadata(resp, ReplaceSymbolResponseMetadata{
				FilePath:   params.FilePath,
				OldContent: string(content),
				NewContent: newContent,
				Action:     action,
			})
			return withFileMutations(resp, params.FilePath), nil
		},
	)
}

// findSymbolByName searches for a symbol by name in the document symbol tree.
func findSymbolByName(symbols []protocol.DocumentSymbolResult, name string) protocol.DocumentSymbolResult {
	for _, sym := range symbols {
		if sym.GetName() == name {
			return sym
		}
		if ds, ok := sym.(*protocol.DocumentSymbol); ok && len(ds.Children) > 0 {
			children := make([]protocol.DocumentSymbolResult, len(ds.Children))
			for i := range ds.Children {
				children[i] = &ds.Children[i]
			}
			if found := findSymbolByName(children, name); found != nil {
				return found
			}
		}
	}
	return nil
}
