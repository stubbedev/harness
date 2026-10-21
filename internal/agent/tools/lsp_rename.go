package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/stubbedev/harness/internal/presence"

	"charm.land/fantasy"

	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
	lsputil "github.com/stubbedev/harness/internal/lsp/util"
)

type renameParams struct {
	Symbol  string
	NewName string
	Root    absPath
}

func renameAction(
	lspManager *lsp.Manager,
	files history.Service,
	tracker filetracker.Service,
	reg *presence.Registry,
) func(context.Context, renameParams) (fantasy.ToolResponse, error) {
	return func(ctx context.Context, params renameParams) (fantasy.ToolResponse, error) {
		if params.NewName == "" {
			return fantasy.NewTextErrorResponse("new_name is required"), nil
		}
		resolved, resp, ok := resolveSymbolTool(ctx, lspManager, params.Symbol, params.Root, resolveSymbol)
		if !ok {
			return resp, nil
		}

		edit, err := resolved.client.Rename(ctx, resolved.path, resolved.line, resolved.char, params.NewName)
		if err != nil {
			slog.Error("Failed to rename symbol", "error", err, "symbol", params.Symbol)
			return fantasy.NewTextErrorResponse(fmt.Sprintf("rename failed: %s", err)), nil
		}
		if edit == nil {
			return fantasy.NewTextResponse(fmt.Sprintf("No rename edits generated for symbol '%s'", params.Symbol)), nil
		}

		sessionID := GetSessionFromContext(ctx)
		affectedFiles := collectAffectedFiles(edit)

		// The lsp tool runs in parallel with edit and write, which take a
		// file's edit lock around their read-modify-write. A rename is the
		// same thing across many files, so it holds all of theirs from the
		// history snapshot until it returns: otherwise an edit landing
		// mid-apply is silently overwritten, or recorded in history as
		// part of the rename.
		unlock := lockFiles(affectedFiles...)
		defer unlock()

		recordHistory := files != nil && sessionID != ""
		before := make(map[string]string, len(affectedFiles))
		if recordHistory {
			for _, path := range affectedFiles {
				content, err := os.ReadFile(path)
				if err != nil {
					slog.Warn("Failed to read file for version tracking", "path", path, "error", err)
					continue
				}
				before[path] = string(content)
			}
		}

		encoding := resolved.client.GetOffsetEncoding()
		if err := lsputil.ApplyWorkspaceEdit(*edit, encoding); err != nil {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to apply rename edits: %s", err)), nil
		}

		changes := make([]lsp.FileChange, 0, len(affectedFiles))
		for _, path := range affectedFiles {
			after, err := os.ReadFile(path)
			if err != nil {
				slog.Warn("Failed to read renamed file", "path", path, "error", err)
				continue
			}
			changes = append(changes, lsp.FileChange{Path: path, Content: string(after)})
			old, tracked := before[path]
			if !tracked {
				continue
			}
			if err := recordFileVersion(ctx, files, sessionID, path, old, string(after)); err != nil {
				slog.Warn("Failed to record file history for rename", "path", path, "error", err)
			}
		}

		if tracker != nil && sessionID != "" {
			for _, path := range affectedFiles {
				if _, strict := tracker.(filetracker.Evidence); !strict {
					tracker.RecordRead(ctx, sessionID, path)
				}
			}
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Renamed '%s' to '%s' in %d file(s):\n\n", params.Symbol, params.NewName, len(affectedFiles))
		for _, f := range affectedFiles {
			fmt.Fprintf(&b, "  %s\n", f)
		}

		// A rename knows exactly which files it touched, so the servers
		// hear about those rather than every open file, and every one of
		// them counts as the file in hand: a rename that breaks a caller
		// breaks it in one of these, and burying that under "project
		// diagnostics" reads as unrelated.
		text := b.String() + "\n" + finishFileChange(ctx, lspManager, reg, changes...)

		return withFileMutations(fantasy.NewTextResponse(text), affectedFiles...), nil
	}
}
