package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/filetracker"
)

func diffHeader(label string) string {
	return "--- a/" + strings.TrimPrefix(label, "/") + "\n+++ b/" + strings.TrimPrefix(label, "/") + "\n"
}

func TestDiffLabel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workingDir := filepath.Join(root, "work")
	sibling := filepath.Join(root, "workshop", "a.go")
	require.Equal(t, "sub/a.go", diffLabel(workingDir, filepath.Join(workingDir, "sub", "a.go")))
	require.Equal(t, "sub/a.go", diffLabel(workingDir, filepath.Join("sub", "a.go")))
	require.Equal(t, filepath.ToSlash(sibling), diffLabel(workingDir, sibling))
}

func TestWriteMetadataDiffLabelIsWorkdirRelative(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workingDir := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(filepath.Join(workingDir, "sub"), 0o755))
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	write := NewWriteTool(nil, &mockHistoryService{}, filetracker.NewService(nil), nil, workingDir)

	inside := filepath.Join(workingDir, "sub", "x.go")
	sibling := filepath.Join(root, "workshop", "x.go")
	for path, label := range map[string]string{inside: "sub/x.go", sibling: filepath.ToSlash(sibling)} {
		resp := runFileTool(t, write, ctx, WriteParams{FilePath: path, Content: "package x\n"})
		require.False(t, resp.IsError, resp.Content)
		var meta WriteResponseMetadata
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
		require.True(t, strings.HasPrefix(meta.Diff, diffHeader(label)), meta.Diff)
		require.Equal(t, 1, meta.Additions)
	}
}

func TestReplaceSymbolMetadataDiffLabelIsWorkdirRelative(t *testing.T) {
	t.Parallel()
	workingDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workingDir, "sub"), 0o755))
	path := writeViewFixture(t, filepath.Join(workingDir, "sub"), "x.go", "package x\n\nfunc f() {}\n")
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	tracker := filetracker.NewService(nil)
	require.False(t, runViewTool(t, NewViewTool(nil, tracker, nil, workingDir), ctx, ViewParams{FilePath: path}).IsError)

	params := ReplaceSymbolParams{Symbol: "f", FilePath: path, Action: "delete"}
	rng := protocol.Range{Start: protocol.Position{Line: 2}, End: protocol.Position{Line: 2}}
	resp, err := spliceSymbol(ctx, nil, nil, tracker, nil, workingDir, params, "delete", replaceSymbolOps["delete"], rng)
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	var meta ReplaceSymbolResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, strings.HasPrefix(meta.Diff, diffHeader("sub/x.go")), meta.Diff)
	require.Equal(t, path, meta.FilePath)
	require.Equal(t, "package x\n\nfunc f() {}\n", meta.OldContent)
	require.Equal(t, "package x\n\n", meta.NewContent)
}
