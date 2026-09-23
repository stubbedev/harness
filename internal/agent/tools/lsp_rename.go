package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"charm.land/fantasy"

	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
	lsputil "github.com/stubbedev/harness/internal/lsp/util"
)

type RenameParams struct {
	Symbol  string `json:"symbol" description:"The symbol name to rename"`
	NewName string `json:"new_name" description:"The new name for the symbol"`
	Path    string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
}

func renameAction(
	lspManager *lsp.Manager,
	files history.Service,
	tracker filetracker.Service,
) func(context.Context, RenameParams) (fantasy.ToolResponse, error) {
	return func(ctx context.Context, params RenameParams) (fantasy.ToolResponse, error) {
		if params.NewName == "" {
			return fantasy.NewTextErrorResponse("new_name is required"), nil
		}
		resolved, resp, ok := resolveSymbolTool(ctx, lspManager, params.Symbol, params.Path, resolveSymbol)
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

		for path, old := range before {
			after, err := os.ReadFile(path)
			if err != nil {
				slog.Warn("Failed to read renamed file for version tracking", "path", path, "error", err)
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

		// A rename knows exactly which files it touched, so tell the
		// servers about those rather than re-sending every open file in
		// the workspace and asking for a full re-analysis.
		lspManager.NotifyChangesAsync(ctx, affectedFiles...)

		var b strings.Builder
		fmt.Fprintf(&b, "Renamed '%s' to '%s' in %d file(s):\n\n", params.Symbol, params.NewName, len(affectedFiles))
		for _, f := range affectedFiles {
			fmt.Fprintf(&b, "  %s\n", f)
		}

		// Every file the rename touched counts as the file in hand: a
		// rename that breaks a caller breaks it in one of these, and
		// burying that under "project diagnostics" reads as unrelated.
		text := b.String() + "\n" + reportDiagnosticsNow(ctx, lspManager, affectedFiles...)

		return withFileMutations(fantasy.NewTextResponse(text), affectedFiles...), nil
	}
}
