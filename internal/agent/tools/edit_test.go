package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/filetracker"
)

type mockEditFileTracker struct {
	lastRead time.Time
	reads    []string
}

func (m *mockEditFileTracker) RecordRead(ctx context.Context, sessionID, path string) {
	m.reads = append(m.reads, path)
}

func (m *mockEditFileTracker) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	return m.lastRead
}

func (m *mockEditFileTracker) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return m.reads, nil
}

// Edits to files past the content cap persist the unified diff instead
// of the whole file twice, so a session's history no longer grows by two
// files per edit; small files keep the contents their renderers read.
func TestEditMetadataCapsWholeFileContents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	big := "marker\n" + strings.Repeat("line\n", 20000)
	bigPath := filepath.Join(dir, "big.txt")
	require.NoError(t, os.WriteFile(bigPath, []byte(big), 0o644))
	smallPath := filepath.Join(dir, "small.txt")
	require.NoError(t, os.WriteFile(smallPath, []byte("small\n"), 0o644))

	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	edit := NewEditTool(nil, &mockHistoryService{}, filetracker.NewService(nil), nil, dir)

	resp := runFileTool(t, edit, ctx, EditParams{FilePath: bigPath, Edits: []EditOperation{{OldString: "marker", NewString: "MARKER"}}})
	require.False(t, resp.IsError, resp.Content)
	var bigMeta EditResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &bigMeta))
	require.NotEmpty(t, bigMeta.Diff)
	require.LessOrEqual(t, len(bigMeta.Diff), maxMetadataDiffBytes)
	require.Equal(t, 1, bigMeta.Additions)
	require.Equal(t, 1, bigMeta.Removals)
	require.Empty(t, bigMeta.OldContent)
	require.Empty(t, bigMeta.NewContent)

	resp = runFileTool(t, edit, ctx, EditParams{FilePath: smallPath, Edits: []EditOperation{{OldString: "small", NewString: "SMALL"}}})
	require.False(t, resp.IsError, resp.Content)
	var smallMeta EditResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &smallMeta))
	require.Equal(t, "small\n", smallMeta.OldContent)
	require.Equal(t, "SMALL\n", smallMeta.NewContent)
	require.NotEmpty(t, smallMeta.Diff)
}

func TestEditPreservesCRLFAndMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\r\nbeta\r\n"), 0o644))

	tracker := &mockEditFileTracker{lastRead: time.Now().Add(time.Second)}
	edit := editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		files:       &mockHistoryService{},
		filetracker: tracker,
		workingDir:  dir,
	}

	resp, err := processEditExistingFile(edit, EditParams{
		FilePath: filePath,
		Edits:    []EditOperation{{OldString: "beta", NewString: "BETA"}},
	})
	require.NoError(t, err)
	require.False(t, resp.IsError)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\r\nBETA\r\n", string(content), "the file keeps CRLF line endings on disk")
	require.Equal(t, []string{filePath}, tracker.reads)

	var meta EditResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, "alpha\nbeta\n", meta.OldContent)
	require.Equal(t, "alpha\nBETA\n", meta.NewContent)
}

func TestEditRejectsMultipleMatchesWithoutReplaceAll(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\nalpha\n"), 0o644))

	edit := editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		files:       &mockHistoryService{},
		filetracker: &mockEditFileTracker{lastRead: time.Now().Add(time.Second)},
		workingDir:  dir,
	}

	resp, err := processEditExistingFile(edit, EditParams{
		FilePath: filePath,
		Edits:    []EditOperation{{OldString: "alpha\n", NewString: ""}},
	})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "all 1 edit(s) failed")

	var meta EditResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Len(t, meta.EditsFailed, 1)
	require.Equal(t, 1, meta.EditsFailed[0].Index)
	require.Contains(t, meta.EditsFailed[0].Error, "appears multiple times")

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\nbeta\nalpha\n", string(content))
}
