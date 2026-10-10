package tools

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/lsp"
)

// promptly bounds how long a call that must not wait on a language server may
// take. The waits it replaces were a second at the least and five at the
// most, so anything under this is a call that did not wait.
const promptly = 500 * time.Millisecond

// newDiagnosticsManager returns a manager over one fake client handling every
// file under a fresh directory, with automatic server start-up off so nothing
// real is ever spawned.
func newDiagnosticsManager(t *testing.T) (*lsp.Manager, *lsp.Client, string) {
	t.Helper()
	dir := t.TempDir()
	off := false
	cfg := &config.Config{Options: &config.Options{AutoLSP: &off}}
	manager := lsp.NewManager(config.NewTestStoreWithWorkingDir(cfg, dir))
	client := lsp.NewTestClient("fake", dir)
	manager.Clients().Set("fake", client)
	return manager, client, dir
}

func sessionContext(t *testing.T, session string) context.Context {
	t.Helper()
	return context.WithValue(t.Context(), SessionIDContextKey, session)
}

// The diagnostics action answers with what the servers hold, at once, even
// while a server is still answering and would go on answering for good.
func TestDiagnosticsActionDoesNotWaitForASettle(t *testing.T) {
	t.Parallel()

	manager, client, dir := newDiagnosticsManager(t)
	file := filepath.Join(dir, "main.go")
	client.PublishTestDiagnostics(file, []protocol.Diagnostic{diag(2, protocol.SeverityError, "undefined: x")})
	release := manager.HoldTestSettle()
	t.Cleanup(release)

	start := time.Now()
	resp, err := diagnosticsAction(manager)(sessionContext(t, "s1"), DiagnosticsParams{FilePath: file})
	require.NoError(t, err)
	require.Less(t, time.Since(start), promptly, "diagnostics waited on a settle")
	require.Contains(t, resp.Content, "undefined: x")
	require.Contains(t, resp.Content, diagnosticsSettlingNote)
}

// With no server running for the file, the action does not wait for one to
// start; it says one is coming and that its findings will be delivered.
func TestDiagnosticsActionDoesNotWaitForAColdStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	off := false
	cfg := &config.Config{Options: &config.Options{AutoLSP: &off}}
	manager := lsp.NewManager(config.NewTestStoreWithWorkingDir(cfg, dir))

	start := time.Now()
	resp, err := diagnosticsAction(manager)(sessionContext(t, "s1"), DiagnosticsParams{FilePath: filepath.Join(dir, "main.go")})
	require.NoError(t, err)
	require.Less(t, time.Since(start), promptly)
	require.Contains(t, resp.Content, "No diagnostics reported.")
	require.Contains(t, resp.Content, diagnosticsStartingNote)
}

// An edit hands its file to the servers and reports what they already hold;
// a server still answering does not hold the edit's result back.
func TestFinishFileChangeDoesNotWaitForASettle(t *testing.T) {
	t.Parallel()

	manager, _, dir := newDiagnosticsManager(t)
	release := manager.HoldTestSettle()
	t.Cleanup(release)

	start := time.Now()
	notes := finishFileChange(sessionContext(t, "s1"), manager, nil, filepath.Join(dir, "main.go"))
	require.Less(t, time.Since(start), promptly, "an edit waited on a settle")
	require.Empty(t, notes, "a report taken mid-answer would be wrong")
}

// What the servers work out after a diagnostics call reaches the model
// through the step sweep: nothing while a server is still answering (and
// without waiting for it), then exactly what changed once it is done.
func TestLaterDiagnosticsArriveInTheNextSweep(t *testing.T) {
	t.Parallel()

	manager, client, dir := newDiagnosticsManager(t)
	ctx := sessionContext(t, "s1")
	file := filepath.Join(dir, "main.go")
	other := filepath.Join(dir, "other.go")
	client.PublishTestDiagnostics(file, []protocol.Diagnostic{diag(2, protocol.SeverityError, "undefined: x")})

	release := manager.HoldTestSettle()
	_, err := diagnosticsAction(manager)(ctx, DiagnosticsParams{FilePath: file})
	require.NoError(t, err)

	// The server goes on to fix one file and break another, and is still
	// answering when the next step begins.
	client.PublishTestDiagnostics(file, nil)
	client.PublishTestDiagnostics(other, []protocol.Diagnostic{diag(7, protocol.SeverityWarning, "unused variable y")})
	start := time.Now()
	require.Empty(t, DiagnosticsSweep(ctx, manager), "a sweep reported a server mid-answer")
	require.Less(t, time.Since(start), promptly, "the sweep waited on a settle")

	release()
	report := DiagnosticsSweep(ctx, manager)
	require.Contains(t, report, "unused variable y")
	require.Contains(t, report, "Resolved: Error: "+file)
	require.NotContains(t, report, "<new_diagnostics>\nError", "an already-listed problem was reported as new")

	// Once delivered, it is not delivered again.
	require.Empty(t, DiagnosticsSweep(ctx, manager))
}
