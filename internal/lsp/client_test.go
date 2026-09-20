package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/env"
)

func TestClient(t *testing.T) {
	// Create a simple config for testing
	cfg := config.LSPConfig{
		Command:   "$THE_CMD", // Use echo as a dummy command that won't fail
		Args:      []string{"hello"},
		FileTypes: []string{"go"},
		Env:       map[string]string{},
	}

	// Test creating a powernap client - this will likely fail with echo
	// but we can still test the basic structure
	client, err := New("test", cfg, config.NewShellVariableResolver(env.NewFromMap(map[string]string{
		"THE_CMD": "echo",
	})), ".", false)
	if err != nil {
		// Expected to fail with echo command, skip the rest
		t.Skipf("Powernap client creation failed as expected with dummy command: %v", err)
		return
	}

	// If we get here, test basic interface methods
	if client.GetName() != "test" {
		t.Errorf("Expected name 'test', got '%s'", client.GetName())
	}

	if !client.HandlesFile("test.go") {
		t.Error("Expected client to handle .go files")
	}

	if client.HandlesFile("test.py") {
		t.Error("Expected client to not handle .py files")
	}

	// Test server state
	client.SetServerState(StateReady)
	if client.GetServerState() != StateReady {
		t.Error("Expected server state to be StateReady")
	}

	// Clean up - expect this to fail with echo command
	if err := client.Close(t.Context()); err != nil {
		// Expected to fail with echo command
		t.Logf("Close failed as expected with dummy command: %v", err)
	}
}

// TestNew_ExpansionFailure_Args pins that a failing $(cmd) in LSP
// args surfaces as a load error prefixed "invalid lsp args:" and that
// no client is returned. Mirrors the MCP contract where expansion
// failure hard-stops transport creation rather than silently running
// with an empty or literal value.
func TestNew_ExpansionFailure_Args(t *testing.T) {
	t.Parallel()

	cfg := config.LSPConfig{
		Command: "echo",
		Args:    []string{"--root", "$(false)"},
	}
	resolver := config.NewShellVariableResolver(env.NewFromMap(map[string]string{}))

	client, err := New("test-args-fail", cfg, resolver, ".", false)
	require.Error(t, err)
	require.Nil(t, client, "client must not start when args expansion fails")
	require.Contains(t, err.Error(), "invalid lsp args")
}

// TestNew_ExpansionFailure_Env pins the same contract for env values.
func TestNew_ExpansionFailure_Env(t *testing.T) {
	t.Parallel()

	cfg := config.LSPConfig{
		Command: "echo",
		Env:     map[string]string{"BAD": "$(false)"},
	}
	resolver := config.NewShellVariableResolver(env.NewFromMap(map[string]string{}))

	client, err := New("test-env-fail", cfg, resolver, ".", false)
	require.Error(t, err)
	require.Nil(t, client, "client must not start when env expansion fails")
	require.Contains(t, err.Error(), "invalid lsp env")
}

func TestNilClient(t *testing.T) {
	t.Parallel()

	var c *Client

	require.False(t, c.HandlesFile("/some/file.go"))
	require.Equal(t, DiagnosticCounts{}, c.GetDiagnosticCounts())
	require.Nil(t, c.GetDiagnostics())
	require.Nil(t, c.OpenFileOnDemand(context.Background(), "/some/file.go"))
	require.Nil(t, c.NotifyChange(context.Background(), "/some/file.go"))
	c.WaitForDiagnostics(context.Background(), time.Second)
}

func newTestClient() *Client {
	c := &Client{
		name:        "test",
		diagnostics: csync.NewVersionedMap[protocol.DocumentURI, []protocol.Diagnostic](),
		openFiles:   csync.NewMap[string, *OpenFileInfo](),
	}
	c.serverState.Store(StateStopped)
	return c
}

func TestWaitForDiagnostics_NoChange(t *testing.T) {
	t.Parallel()

	c := newTestClient()
	start := time.Now()
	c.WaitForDiagnostics(t.Context(), 5*time.Second)
	elapsed := time.Since(start)

	// Should return early via firstChangeDeadline (~1s), not the full timeout.
	require.Less(t, elapsed, 2*time.Second, "should return early when no diagnostics change")
}

func TestWaitForDiagnostics_ImmediateChange(t *testing.T) {
	t.Parallel()

	c := newTestClient()

	go func() {
		time.Sleep(100 * time.Millisecond)
		c.diagnostics.Set(protocol.DocumentURI("file:///test.go"), nil)
	}()

	start := time.Now()
	c.WaitForDiagnostics(t.Context(), 5*time.Second)
	elapsed := time.Since(start)

	// Should detect the change and then settle (~300ms settle + overhead).
	require.Less(t, elapsed, 2*time.Second, "should return after settling, not full timeout")
	require.Greater(t, elapsed, 200*time.Millisecond, "should wait for settle duration")
}

func TestWaitForDiagnostics_RepeatedChanges(t *testing.T) {
	t.Parallel()

	c := newTestClient()

	// Simulate an LSP server that publishes diagnostics in bursts.
	go func() {
		for i := range 5 {
			time.Sleep(50 * time.Millisecond)
			c.diagnostics.Set(protocol.DocumentURI("file:///test.go"), []protocol.Diagnostic{
				{Message: fmt.Sprintf("diag-%d", i)},
			})
		}
	}()

	start := time.Now()
	c.WaitForDiagnostics(t.Context(), 5*time.Second)
	elapsed := time.Since(start)

	// Should wait for diagnostics to settle after the burst finishes.
	// Burst lasts ~250ms, then 300ms settle window, so total ~550ms+.
	require.Less(t, elapsed, 2*time.Second, "should return after settling, not full timeout")
	require.Greater(t, elapsed, 400*time.Millisecond, "should wait for all changes to settle")
}

func TestWaitForDiagnostics_ContextCancellation(t *testing.T) {
	t.Parallel()

	c := newTestClient()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	c.WaitForDiagnostics(ctx, 5*time.Second)
	elapsed := time.Since(start)

	require.Less(t, elapsed, 1*time.Second, "should return shortly after context cancellation")
}

func TestWaitForDiagnostics_NilClient(t *testing.T) {
	t.Parallel()

	var c *Client
	// Should not panic.
	c.WaitForDiagnostics(context.Background(), time.Second)
}

func TestWaitForDiagnostics_SettlesPromptly(t *testing.T) {
	t.Parallel()

	c := newTestClient()

	go func() {
		time.Sleep(20 * time.Millisecond)
		c.diagnostics.Set(protocol.DocumentURI("file:///test.go"), nil)
	}()

	start := time.Now()
	c.WaitForDiagnostics(t.Context(), 5*time.Second)
	elapsed := time.Since(start)

	// Publication plus one settle window (300ms) and nothing else: the wait is
	// woken by the change, not by a poll interval.
	require.Less(t, elapsed, 450*time.Millisecond, "settle should not add polling slack")
}

func TestCloseVanishedFiles(t *testing.T) {
	t.Parallel()

	c := newTestClient()
	dir := t.TempDir()
	kept := filepath.Join(dir, "kept.go")
	gone := filepath.Join(dir, "gone.go")
	require.NoError(t, os.WriteFile(kept, []byte("package a\n"), 0o644))
	require.NoError(t, os.WriteFile(gone, []byte("package b\n"), 0o644))

	keptURI := string(protocol.URIFromPath(kept))
	goneURI := string(protocol.URIFromPath(gone))
	for _, uri := range []string{keptURI, goneURI} {
		c.openFiles.Set(uri, &OpenFileInfo{Version: 1, URI: protocol.DocumentURI(uri)})
		c.diagnostics.Set(protocol.DocumentURI(uri), []protocol.Diagnostic{{Message: "problem"}})
	}
	require.NoError(t, os.Remove(gone))

	c.closeVanishedFiles(t.Context())

	require.False(t, c.IsFileOpen(gone), "a renamed-away file must not stay open")
	require.True(t, c.IsFileOpen(kept), "a file still on disk stays open")
	require.Empty(t, c.GetFileDiagnostics(protocol.DocumentURI(goneURI)), "stale diagnostics must be dropped")
	require.Len(t, c.GetFileDiagnostics(protocol.DocumentURI(keptURI)), 1)
}

func TestRefreshOpenFilesForgetsVanishedFiles(t *testing.T) {
	t.Parallel()

	c := newTestClient()
	gone := filepath.Join(t.TempDir(), "gone.go")
	require.NoError(t, os.WriteFile(gone, []byte("package a\n"), 0o644))
	uri := string(protocol.URIFromPath(gone))
	c.openFiles.Set(uri, &OpenFileInfo{Version: 1, URI: protocol.DocumentURI(uri)})
	c.diagnostics.Set(protocol.DocumentURI(uri), []protocol.Diagnostic{{Message: "No packages found for open file"}})
	require.NoError(t, os.Remove(gone))

	// Every open file has vanished, so the refresh prunes them without
	// ever reaching the (nil in this test) server connection.
	c.RefreshOpenFiles(t.Context())

	require.False(t, c.IsFileOpen(gone))
	require.Empty(t, c.GetFileDiagnostics(protocol.DocumentURI(uri)))
}
