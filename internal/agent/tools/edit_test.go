package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
