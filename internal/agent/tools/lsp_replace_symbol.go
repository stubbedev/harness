package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/stubbedev/harness/internal/presence"

	"charm.land/fantasy"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/toolaction"
)

type replaceSymbolParams struct {
	Symbol      string
	File        absPath
	Replacement string
	Action      ReplaceSymbolMode
}

type ReplaceSymbolMode string

const (
	ReplaceSymbolReplace   ReplaceSymbolMode = "replace"
	ReplaceSymbolAddBefore ReplaceSymbolMode = "add_before"
	ReplaceSymbolAddAfter  ReplaceSymbolMode = "add_after"
	ReplaceSymbolDelete    ReplaceSymbolMode = "delete"
)

// ReplaceSymbolResponseMetadata carries diff data for the renderer.
type ReplaceSymbolResponseMetadata struct {
	FilePath string `json:"file_path"`
	Diff     string `json:"diff,omitempty"`
	// OldContent and NewContent remain for results persisted before the
	// stored diff took over; new results fill them only for small files.
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
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

var replaceSymbolOps = map[ReplaceSymbolMode]replaceSymbolOp{
	ReplaceSymbolReplace: {
		span:   func(start, end int) (int, int) { return start, end + 1 },
		insert: true,
		summary: func(symbol, path string, start, end int) string {
			return fmt.Sprintf("Replaced symbol '%s' in %s (lines %d-%d)", symbol, path, start, end)
		},
	},
	ReplaceSymbolAddBefore: {
		span:   func(start, _ int) (int, int) { return start, start },
		insert: true,
		summary: func(symbol, path string, start, _ int) string {
			return fmt.Sprintf("Inserted before symbol '%s' in %s (before line %d)", symbol, path, start)
		},
	},
	ReplaceSymbolAddAfter: {
		span:   func(_, end int) (int, int) { return end + 1, end + 1 },
		insert: true,
		summary: func(symbol, path string, _, end int) string {
			return fmt.Sprintf("Inserted after symbol '%s' in %s (after line %d)", symbol, path, end)
		},
	},
	ReplaceSymbolDelete: {
		span: func(start, end int) (int, int) { return start, end + 1 },
		summary: func(symbol, path string, start, end int) string {
			return fmt.Sprintf("Deleted symbol '%s' from %s (lines %d-%d)", symbol, path, start, end)
		},
	},
}

var replaceSymbolModes = toolaction.New("mode", replaceSymbolOps)

func replaceSymbolAction(
	lspManager *lsp.Manager,
	files history.Service,
	tracker filetracker.Service,
	reg *presence.Registry,
	workingDir string,
) func(context.Context, replaceSymbolParams) (fantasy.ToolResponse, error) {
	return func(ctx context.Context, params replaceSymbolParams) (fantasy.ToolResponse, error) {
		if params.Symbol == "" {
			return fantasy.NewTextErrorResponse("symbol is required"), nil
		}

		action := params.Action
		if action == "" {
			action = ReplaceSymbolReplace
		}
		op, err := replaceSymbolModes.Lookup(action)
		if err != nil {
			return fantasy.NewTextErrorResponse(err.Error()), nil
		}
		if op.insert && params.Replacement == "" {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("replacement is required for mode %q", action)), nil
		}

		file := string(params.File)
		lspManager.Start(ctx, file)

		client := findLSPClient(lspManager, params.File)
		if client == nil {
			return fantasy.NewTextErrorResponse(noLSPClientMessage(lspManager, file)), nil
		}

		symbols, err := client.DocumentSymbols(ctx, file)
		if err != nil {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to get document symbols: %s", err)), nil
		}

		target := findSymbolByName(symbols, params.Symbol)
		if target == nil {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("symbol '%s' not found in %s", params.Symbol, file)), nil
		}

		return spliceSymbol(ctx, lspManager, files, tracker, reg, workingDir, params, action, op, target.GetRange())
	}
}

// spliceSymbol applies op to the lines rng spans in params.File and
// writes the result back, reporting a file it cannot read or write, or
// one changed since it was read, as an error result for the model.
func spliceSymbol(
	ctx context.Context,
	lspManager *lsp.Manager,
	files history.Service,
	tracker filetracker.Service,
	reg *presence.Registry,
	workingDir string,
	params replaceSymbolParams,
	action ReplaceSymbolMode,
	op replaceSymbolOp,
	rng protocol.Range,
) (fantasy.ToolResponse, error) {
	path := string(params.File)
	unlock := lockFile(path)
	defer unlock()
	content, err := os.ReadFile(path)
	if err != nil {
		return fantasy.NewTextErrorResponse(fileAccessFailure("read", path, err)), nil
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
	if err := checkFileEvidence(ctx, tracker, sessionID, path, content, []filetracker.Range{affected}); err != nil {
		return fantasy.NewTextErrorResponse(conflictEvidence(ctx, tracker, sessionID, path, content, affected.Start, err).Error()), nil
	}
	if err := guardedWrite(path, content, []byte(newContent), false); err != nil {
		if current, readErr := os.ReadFile(path); readErr == nil {
			err = conflictEvidence(ctx, tracker, sessionID, path, current, affected.Start, err)
		}
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	if files != nil && sessionID != "" {
		if err := recordFileVersion(ctx, files, sessionID, path, string(content), newContent); err != nil {
			slog.Warn("Failed to record file history for replace", "path", path, "error", err)
		}
	}

	filetracker.Advance(ctx, tracker, sessionID, path, content, []byte(newContent))

	summary := op.summary(params.Symbol, path, startLine+1, endLine+1)

	changes := newEditResponseMetadata(path, workingDir, string(content), newContent)
	meta := ReplaceSymbolResponseMetadata{
		FilePath:   path,
		Diff:       changes.Diff,
		OldContent: changes.OldContent,
		NewContent: changes.NewContent,
		Action:     string(action),
	}
	resp := fantasy.NewTextResponse(summary + "\n" + finishFileChange(ctx, lspManager, reg, lsp.FileChange{Path: path, Content: newContent}))
	resp = fantasy.WithResponseMetadata(resp, meta)
	return withFileMutations(resp, path), nil
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
