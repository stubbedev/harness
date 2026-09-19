package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeViewFixture writes a small text file for the multi-file tests and
// returns its absolute path.
func writeViewFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// TestViewToolMultiFile: the files form reads every entry in one call,
// labels each section with its path, and reports per-file failures as
// sections instead of wasting the whole call.
func TestViewToolMultiFile(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	good := writeViewFixture(t, workingDir, "good.txt", "alpha\nbeta\n")
	other := writeViewFixture(t, workingDir, "other.txt", "gamma\ndelta\n")
	missing := filepath.Join(workingDir, "nope.txt")

	tool := newViewToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp := runViewTool(t, tool, ctx, ViewParams{
		Files: []ViewFileRequest{
			{FilePath: good},
			{FilePath: missing},
			{FilePath: other, Offset: 0, Limit: 1},
		},
	})

	require.False(t, resp.IsError, "one missing file must not fail the call: %q", resp.Content)
	// The renderer quotes paths with %q, so on Windows the separators
	// come back escaped; build the expectation the same way.
	require.Contains(t, resp.Content, "path="+strconv.Quote(good))
	require.Contains(t, resp.Content, "alpha")
	require.Contains(t, resp.Content, "path="+strconv.Quote(missing)+" error")
	require.Contains(t, resp.Content, "File not found")
	require.Contains(t, resp.Content, "gamma")
	require.NotContains(t, resp.Content, "delta", "the per-entry limit applies")
	require.Equal(t, 3, strings.Count(resp.Content, "</file>"), "one section per entry")

	var meta ViewFilesResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Len(t, meta.Files, 2, "only the files that read carry metadata")
	require.Equal(t, good, meta.Files[0].FilePath)
	require.Equal(t, other, meta.Files[1].FilePath)
}

// TestViewToolMultiFileAllFailed: when nothing can be read the call
// fails with the first per-file error, not a bag of error sections.
func TestViewToolMultiFileAllFailed(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	tool := newViewToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp := runViewTool(t, tool, ctx, ViewParams{
		Files: []ViewFileRequest{
			{FilePath: filepath.Join(workingDir, "gone1.txt")},
			{FilePath: filepath.Join(workingDir, "gone2.txt")},
		},
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "File not found")
}

// TestViewToolMultiFileRejectsMixedForms: file_path and files are two
// shapes of one call; sending both is a mistake to name, not to guess
// at.
func TestViewToolMultiFileRejectsMixedForms(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	tool := newViewToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runViewTool(t, tool, ctx, ViewParams{
		FilePath: "some.txt",
		Files:    []ViewFileRequest{{FilePath: "other.txt"}},
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "either file_path or files")
}

// TestViewToolMultiFileCapsEntryCount: the per-call cap keeps one call
// from flooding the context; the error says the limit.
func TestViewToolMultiFileCapsEntryCount(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	tool := newViewToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	files := make([]ViewFileRequest, MaxViewFilesPerCall+1)
	for i := range files {
		files[i] = ViewFileRequest{FilePath: "f.txt"}
	}
	resp := runViewTool(t, tool, ctx, ViewParams{Files: files})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "At most 10 files per call")
}
