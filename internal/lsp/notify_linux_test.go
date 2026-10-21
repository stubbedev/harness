//go:build linux

package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

func newSyncLoggingClient(t *testing.T) (*Client, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "sync.log")
	return newFakeClient(t, map[string]string{fakeServerLogEnv: log}), log
}

func readDocumentSyncs(t *testing.T, log string) []documentSync {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var syncs []documentSync
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		var entry documentSync
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &entry))
		syncs = append(syncs, entry)
	}
	require.NoError(t, scanner.Err())
	return syncs
}

func requireDocumentSyncs(t *testing.T, log string, want []documentSync) {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(readDocumentSyncs(t, log)) >= len(want)
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, want, readDocumentSyncs(t, log))
}

func newSyncManager(t *testing.T, c *Client) *Manager {
	t.Helper()
	off := false
	cfg := &config.Config{Options: &config.Options{AutoLSP: &off}}
	manager := NewManager(config.NewTestStoreWithWorkingDir(cfg, c.cwd))
	manager.Clients().Set(c.name, c)
	return manager
}

func TestSyncContentOpensUnseenFileWithTheGivenText(t *testing.T) {
	t.Parallel()

	c, log := newSyncLoggingClient(t)
	path := filepath.Join(c.cwd, "never_on_disk.go")

	require.NoError(t, c.SyncContent(t.Context(), path, "package a\n"))

	require.True(t, c.IsFileOpen(path))
	requireDocumentSyncs(t, log, []documentSync{
		{Method: "textDocument/didOpen", Version: 1, Text: "package a\n"},
	})
}

func TestSyncContentChangesAnOpenFile(t *testing.T) {
	t.Parallel()

	c, log := newSyncLoggingClient(t)
	path := filepath.Join(c.cwd, "a.go")
	require.NoError(t, os.WriteFile(path, []byte("package a\n"), 0o644))
	require.NoError(t, c.OpenFile(t.Context(), path))

	require.NoError(t, c.SyncContent(t.Context(), path, "package b\n"))

	requireDocumentSyncs(t, log, []documentSync{
		{Method: "textDocument/didOpen", Version: 1, Text: "package a\n"},
		{Method: "textDocument/didChange", Version: 2, Text: "package b\n"},
	})
}

func TestNotifyChangesAsyncSendsAFirstEditOnce(t *testing.T) {
	t.Parallel()

	c, log := newSyncLoggingClient(t)
	manager := newSyncManager(t, c)
	path := filepath.Join(c.cwd, "a.go")
	require.NoError(t, os.WriteFile(path, []byte("stale on disk\n"), 0o644))

	manager.NotifyChangesAsync(t.Context(), FileChange{Path: path, Content: "package a\n"})
	manager.NotifyChangesAsync(t.Context(), FileChange{Path: path, Content: "package b\n"})

	requireDocumentSyncs(t, log, []documentSync{
		{Method: "textDocument/didOpen", Version: 1, Text: "package a\n"},
		{Method: "textDocument/didChange", Version: 2, Text: "package b\n"},
	})
}

func TestNotifyChangeAsyncReadsTheFileFromDisk(t *testing.T) {
	t.Parallel()

	c, log := newSyncLoggingClient(t)
	manager := newSyncManager(t, c)
	path := filepath.Join(c.cwd, "a.go")
	require.NoError(t, os.WriteFile(path, []byte("package a\n"), 0o644))

	manager.NotifyChangeAsync(t.Context(), path)

	requireDocumentSyncs(t, log, []documentSync{
		{Method: "textDocument/didOpen", Version: 1, Text: "package a\n"},
	})
}
