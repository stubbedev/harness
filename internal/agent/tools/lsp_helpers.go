package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/lsp"
)

// resolvedSymbol holds the result of resolving a symbol name to an LSP position.
type resolvedSymbol struct {
	client *lsp.Client
	path   string
	line   int
	char   int
}

// resolveSymbolTool runs the preamble every LSP tool shares: require a
// symbol and resolve it under root. resolve is either resolveSymbol or
// resolveSymbolResults. ok is false when resp carries the shared failure
// response (missing or unresolvable symbol) and the caller must return it.
func resolveSymbolTool[T any](
	ctx context.Context,
	lspManager *lsp.Manager,
	symbol string,
	root absPath,
	resolve func(context.Context, *lsp.Manager, string, absPath) (T, error),
) (T, fantasy.ToolResponse, bool) {
	if symbol == "" {
		var zero T
		return zero, fantasy.NewTextErrorResponse("symbol is required"), false
	}
	resolved, err := resolve(ctx, lspManager, symbol, root)
	if err != nil {
		var zero T
		return zero, fantasy.NewTextErrorResponse(fmt.Sprintf("cannot resolve symbol '%s': %s", symbol, err)), false
	}
	return resolved, fantasy.ToolResponse{}, true
}

// resolveSymbol greps for a symbol name, triggers lazy LSP startup, and
// returns the first match position that a running LSP client confirms
// is a valid identifier. Matches inside comments or strings are skipped
// automatically because the LSP will reject them.
func resolveSymbol(ctx context.Context, lspManager *lsp.Manager, symbol string, root absPath) (*resolvedSymbol, error) {
	results, err := resolveSymbolResults(ctx, lspManager, symbol, root)
	if err != nil {
		return nil, err
	}

	// Try each candidate until the LSP confirms it's a real identifier.
	// This filters out grep matches in comments, strings, or partial
	// identifiers that slipped past the word-boundary filter.
	for _, r := range results {
		_, err := r.client.Definition(ctx, r.path, r.line, r.char)
		if err == nil || !isNoIdentifierError(err) {
			return r, nil
		}
	}
	// All candidates were rejected by the LSP; return the first one
	// so the caller gets a meaningful error from their own LSP call.
	return results[0], nil
}

// resolveSymbolResults greps for a symbol and returns all viable
// {client, path, position} tuples. Callers that need just one match
// (definition, rename, call hierarchy) use resolveSymbol; callers that
// want to iterate all matches (references) use this directly.
func resolveSymbolResults(ctx context.Context, lspManager *lsp.Manager, symbol string, root absPath) ([]*resolvedSymbol, error) {
	lspManager.Start(ctx, string(root))

	// Use word boundaries to avoid matching inside larger identifiers
	// (e.g. "Bar" inside "myBar"). The symbol is already QuoteMeta'd
	// so dots and other regex metacharacters are escaped.
	pattern := `\b` + regexp.QuoteMeta(symbol) + `\b`
	matches, err := searchFiles(ctx, pattern, string(root), "", 100)
	if err != nil {
		return nil, fmt.Errorf("searching %s failed: %w", root, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no file under %s mentions it: check the spelling, or widen path", root)
	}

	var results []*resolvedSymbol
	for _, match := range matches {
		client := findLSPClient(lspManager, absPath(match.path))
		if client == nil {
			continue
		}

		results = append(results, &resolvedSymbol{
			client: client,
			path:   match.path,
			line:   match.lineNum,
			// The client takes a 1-based column in the server's
			// negotiated encoding; the search reports bytes.
			char: lsp.CharacterOffset(match.rawLine, match.charNum-1+getSymbolOffset(symbol), client.GetOffsetEncoding()) + 1,
		})
	}

	if len(results) == 0 {
		return nil, errors.New(noLSPClientMessage(lspManager, "any file under "+string(root)+" that mentions it"))
	}
	return results, nil
}

func noLSPClientMessage(lspManager *lsp.Manager, subject string) string {
	running := slices.Sorted(maps.Keys(maps.Collect(lspManager.Clients().Seq2())))
	state := "no client is running"
	if len(running) > 0 {
		state = "running: " + strings.Join(running, ", ")
	}
	return fmt.Sprintf("no LSP client handles %s (%s). If a server should handle it, restart it with action restart; otherwise read the code with view and grep instead", subject, state)
}

// findLSPClient returns the first LSP client that handles the given file path.
func findLSPClient(lspManager *lsp.Manager, path absPath) *lsp.Client {
	for c := range lspManager.Clients().Seq() {
		if c.HandlesFile(string(path)) {
			return c
		}
	}
	return nil
}

// collectAffectedFiles extracts all unique file paths from a WorkspaceEdit.
func collectAffectedFiles(edit *protocol.WorkspaceEdit) []string {
	seen := make(map[string]struct{})
	var files []string

	for uri := range edit.Changes {
		path, err := uri.Path()
		if err != nil {
			continue
		}
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			files = append(files, path)
		}
	}

	addURI := func(uri protocol.DocumentURI) {
		path, err := uri.Path()
		if err != nil {
			return
		}
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			files = append(files, path)
		}
	}

	for _, change := range edit.DocumentChanges {
		switch {
		case change.TextDocumentEdit != nil:
			addURI(change.TextDocumentEdit.TextDocument.URI)
		case change.CreateFile != nil:
			addURI(change.CreateFile.URI)
		case change.RenameFile != nil:
			addURI(change.RenameFile.OldURI)
			addURI(change.RenameFile.NewURI)
		case change.DeleteFile != nil:
			addURI(change.DeleteFile.URI)
		}
	}

	return files
}

// isNoIdentifierError checks if an error indicates the grep match was not
// actually an identifier (e.g., matched inside a comment or string).
func isNoIdentifierError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no identifier found")
}

// getSymbolOffset returns the character offset to the actual symbol name
// in a qualified symbol (e.g., "Bar" in "foo.Bar" or "method" in "Class::method").
func getSymbolOffset(symbol string) int {
	if idx := strings.LastIndex(symbol, "::"); idx != -1 {
		return idx + 2
	}
	if idx := strings.LastIndex(symbol, "."); idx != -1 {
		return idx + 1
	}
	if idx := strings.LastIndex(symbol, "\\"); idx != -1 {
		return idx + 1
	}
	return 0
}

// cleanupLocations deduplicates and sorts a slice of LSP locations.
func cleanupLocations(locations []protocol.Location) []protocol.Location {
	slices.SortFunc(locations, func(a, b protocol.Location) int {
		if a.URI != b.URI {
			return strings.Compare(string(a.URI), string(b.URI))
		}
		if a.Range.Start.Line != b.Range.Start.Line {
			return cmp.Compare(a.Range.Start.Line, b.Range.Start.Line)
		}
		return cmp.Compare(a.Range.Start.Character, b.Range.Start.Character)
	})
	return slices.CompactFunc(locations, func(a, b protocol.Location) bool {
		return a.URI == b.URI &&
			a.Range.Start.Line == b.Range.Start.Line &&
			a.Range.Start.Character == b.Range.Start.Character
	})
}
