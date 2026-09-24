package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/filetracker"
)

// TestEditAndWriteMetadataMatchReadBackMutations pins the metadata the edit
// and write tools produce, now that they hash the content they wrote, to
// what they produced by marshalling their metadata without file_mutations
// and letting withFileMutations read the file back and append the key.
func TestEditAndWriteMetadataMatchReadBackMutations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	tracker := filetracker.NewService(nil)
	view := NewViewTool(nil, tracker, nil, dir)
	edit := NewEditTool(nil, &mockHistoryService{}, tracker, dir)
	write := NewWriteTool(nil, &mockHistoryService{}, tracker, dir)

	readBack := func(t *testing.T, resp fantasy.ToolResponse, path string, metadata any, clear func()) {
		t.Helper()
		require.False(t, resp.IsError, resp.Content)
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), metadata))
		clear()
		want := withFileMutations(fantasy.WithResponseMetadata(fantasy.ToolResponse{}, metadata), path)
		require.Equal(t, want.Metadata, resp.Metadata)
	}

	lf := writeViewFixture(t, dir, "lf.go", "package main\n\nfunc a() {\n\tx := 1\n}\n")
	crlf := writeViewFixture(t, dir, "crlf.go", "package main\r\n\r\nfunc b() {\r\n\ty := 2\r\n}\r\n")
	for _, path := range []string{lf, crlf} {
		require.False(t, runViewTool(t, view, ctx, ViewParams{FilePath: path}).IsError)
	}
	cases := map[string]EditParams{
		"exact":      {FilePath: lf, Edits: []EditOperation{{OldString: "x := 1", NewString: "x := 10"}}},
		"whitespace": {FilePath: lf, Edits: []EditOperation{{OldString: "    x := 10", NewString: "    x := 100"}}},
		"crlf":       {FilePath: crlf, Edits: []EditOperation{{OldString: "y := 2", NewString: "y := 20"}}},
		"partial": {FilePath: lf, Edits: []EditOperation{
			{OldString: "func a() {", NewString: "func aa() {"},
			{OldString: "not in the file", NewString: "x"},
		}},
		"create": {FilePath: filepath.Join(dir, "new.go"), Edits: []EditOperation{{NewString: "package new\n"}}},
	}
	for _, name := range []string{"exact", "whitespace", "crlf", "partial", "create"} {
		params := cases[name]
		var meta EditResponseMetadata
		readBack(t, runFileTool(t, edit, ctx, params), params.FilePath, &meta, func() {
			require.Len(t, meta.FileMutations, 1, name)
			meta.FileMutations = nil
		})
	}

	var meta WriteResponseMetadata
	readBack(t, runFileTool(t, write, ctx, WriteParams{FilePath: lf, Content: "package main\n"}), lf, &meta, func() {
		require.Len(t, meta.FileMutations, 1)
		meta.FileMutations = nil
	})
	fresh := filepath.Join(dir, "fresh.go")
	readBack(t, runFileTool(t, write, ctx, WriteParams{FilePath: fresh, Content: "package fresh\n"}), fresh, &meta, func() {
		require.Len(t, meta.FileMutations, 1)
		meta.FileMutations = nil
	})
}

func TestFileMutationMetadataPreservesRendererMetadata(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "main.go")
	content := []byte("package main\n")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	response := withFileMutations(fantasy.ToolResponse{Content: "written", Metadata: `{"additions":1}`}, path)
	var metadata struct {
		Additions int            `json:"additions"`
		Mutations []fileMutation `json:"file_mutations"`
	}
	require.NoError(t, json.Unmarshal([]byte(response.Metadata), &metadata))
	require.Equal(t, 1, metadata.Additions)
	require.Len(t, metadata.Mutations, 1)
	digest := sha256.Sum256(content)
	require.Equal(t, fileMutation{Path: path, Version: hex.EncodeToString(digest[:])[:12]}, metadata.Mutations[0], "the version is the first twelve hex digits of the content digest")
}
