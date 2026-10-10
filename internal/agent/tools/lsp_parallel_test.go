package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// callLSP runs one lsp action through the tool, as the agent loop would. It
// reports failure as an error response rather than through a testing.T, so
// it can run on a goroutine of its own.
func callLSP(ctx context.Context, tool fantasy.AgentTool, action LSPAction) fantasy.ToolResponse {
	input, err := json.Marshal(LSPParams{Action: string(action)})
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error())
	}
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: LSPToolName, Input: string(input)})
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error())
	}
	return resp
}

// The lsp tool is parallel, so a definition lookup does not hold up the
// rest of its step; mutual exclusion lives inside it, for mutations only.
func TestLSPToolIsParallel(t *testing.T) {
	t.Parallel()
	require.True(t, NewLSPTool(nil, nil, nil, nil).Info().Parallel)

	for action := range lspActions(nil, nil, nil, nil) {
		mutates := action == LSPActionRename || action == LSPActionReplaceSymbol || action == LSPActionRestart
		require.Equal(t, mutates, lspMutatingActions[action], "%s", action)
	}
}

// Mutating lsp actions take turns; read-only ones never wait for them.
func TestLSPMutationsSerialiseAndReadsDoNot(t *testing.T) {
	t.Parallel()

	// Each test gets its own manager, so its lock is its own.
	manager, _, _ := newDiagnosticsManager(t)

	var inMutation atomic.Int32
	var overlapped atomic.Bool
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	mutate := func(context.Context, LSPParams) (fantasy.ToolResponse, error) {
		if inMutation.Add(1) > 1 {
			overlapped.Store(true)
		}
		entered <- struct{}{}
		<-release
		inMutation.Add(-1)
		return fantasy.NewTextResponse("mutated"), nil
	}
	read := func(context.Context, LSPParams) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("read"), nil
	}
	tool := newLSPTool(manager, nil, map[LSPAction]lspActionFunc{
		LSPActionRename:        mutate,
		LSPActionReplaceSymbol: mutate,
		LSPActionDefinition:    read,
		LSPActionReferences:    read,
	})

	done := make(chan string, 2)
	go func() { done <- callLSP(t.Context(), tool, LSPActionRename).Content }()
	<-entered

	go func() { done <- callLSP(t.Context(), tool, LSPActionReplaceSymbol).Content }()
	select {
	case <-entered:
		t.Fatal("a second mutation started while the first held the lock")
	case <-time.After(100 * time.Millisecond):
	}

	// Reads go straight through while a mutation holds the lock.
	start := time.Now()
	require.Equal(t, "read", callLSP(t.Context(), tool, LSPActionDefinition).Content)
	require.Equal(t, "read", callLSP(t.Context(), tool, LSPActionReferences).Content)
	require.Less(t, time.Since(start), promptly, "a read waited on a mutation")

	close(release)
	require.Equal(t, "mutated", <-done)
	require.Equal(t, "mutated", <-done)
	require.False(t, overlapped.Load(), "two mutations ran at once")
}

// A mutation queued behind another gives up when its call is cancelled
// rather than waiting the other out.
func TestLSPMutationWaitHonoursCancellation(t *testing.T) {
	t.Parallel()

	manager, _, _ := newDiagnosticsManager(t)
	unlock, err := lockLSPMutation(t.Context(), manager)
	require.NoError(t, err)
	defer unlock()

	ran := false
	tool := newLSPTool(manager, nil, map[LSPAction]lspActionFunc{
		LSPActionRestart: func(context.Context, LSPParams) (fantasy.ToolResponse, error) {
			ran = true
			return fantasy.NewTextResponse("restarted"), nil
		},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	resp := callLSP(ctx, tool, LSPActionRestart)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "cancelled while waiting")
	require.False(t, ran)
}

// lockFiles takes each stripe once, so two paths that hash to one stripe do
// not deadlock it, and it excludes a single-file locker on any of them.
func TestLockFilesSharesStripesAndExcludesSingleLocks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := filepath.Join(dir, "a.go")
	var twin string
	for i := range 100 * len(fileLocks) {
		candidate := filepath.Join(dir, fmt.Sprintf("f%d.go", i))
		if fileLockIndex(candidate) == fileLockIndex(first) {
			twin = candidate
			break
		}
	}
	require.NotEmpty(t, twin, "no path sharing a stripe found")

	unlock := lockFiles(first, twin, first)

	acquired := make(chan struct{})
	go func() {
		release := lockFile(twin)
		close(acquired)
		release()
	}()
	select {
	case <-acquired:
		t.Fatal("a single-file lock got in while lockFiles held the stripe")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("lockFiles did not release its stripes")
	}
}
