package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/subagents"
)

// registerTestRun puts a background run on the coordinator the way a real
// background dispatch would, without running a child.
func registerTestRun(coord *coordinator, parentSession, name string) *backgroundRun {
	run := &backgroundRun{
		handle:        coord.newBackgroundHandle(),
		childSession:  "child-" + parentSession,
		parentSession: parentSession,
		agentName:     name,
	}
	coord.backgroundRuns.Set(run.handle, run)
	coord.backgroundByChild.Set(run.childSession, run.handle)
	return run
}

func waitInput(t *testing.T, handles []string, timeoutSeconds *int) fantasy.ToolCall {
	t.Helper()
	input := map[string]any{"handles": handles}
	if timeoutSeconds != nil {
		input["timeout_seconds"] = *timeoutSeconds
	}
	data, err := json.Marshal(input)
	require.NoError(t, err)
	return fantasy.ToolCall{Input: string(data)}
}

// runWait drives the waiting form of the agent tool: a call with no prompt.
func runWait(t *testing.T, coord *coordinator, ctx context.Context, sessionID string, call fantasy.ToolCall) fantasy.ToolResponse {
	t.Helper()
	tool := waitTool(t, coord)
	resp, err := tool.Run(sessionCtx(ctx, sessionID), call)
	require.NoError(t, err)
	return resp
}

// waitTool is the agent dispatcher, which doubles as the sync point for
// background dispatches when called without a prompt.
func waitTool(t *testing.T, coord *coordinator) fantasy.AgentTool {
	t.Helper()
	tool, err := coord.agentTool(t.Context())
	require.NoError(t, err)
	return tool
}

func TestWaitTool_FinishedRunReturnsResult(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("the answer"))

	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{run.handle}, nil))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "All waited agents finished.")
	assert.Contains(t, resp.Content, "fast ("+run.handle+"): completed")
	assert.Contains(t, resp.Content, "the answer")

	// Results stay collectable in later turns.
	resp2 := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{run.handle}, nil))
	assert.Contains(t, resp2.Content, "the answer")
}

func TestWaitTool_FailedRunReportsError(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")
	run.finish(subagents.StatusFailed, fantasy.NewTextErrorResponse("boom"))

	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{run.handle}, nil))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "failed")
	assert.Contains(t, resp.Content, "Error: boom")
}

func TestWaitTool_SnapshotWithZeroTimeout(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")

	zero := 0
	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{run.handle}, &zero))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Snapshot")
	assert.Contains(t, resp.Content, "fast ("+run.handle+"): still running")
}

func TestWaitTool_TimesOut(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")

	one := 1
	start := time.Now()
	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{run.handle}, &one))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Timed out")
	assert.Contains(t, resp.Content, "still running")
	assert.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)
}

func TestWaitTool_ReturnsEarlyOnMessage(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")
	other := registerTestRun(coord, "parent-1", "task")

	// A message from the waited handle arrives while it is still running.
	require.NoError(t, coord.recordLiveSubagentMessage(run, "found it early"))
	// A message from a handle that is not waited on must stay put.
	require.NoError(t, coord.recordLiveSubagentMessage(other, "not for you"))

	one := 30
	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{run.handle}, &one))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Returned early")
	assert.Contains(t, resp.Content, "found it early")
	assert.Contains(t, resp.Content, "still running")

	// Delivered exactly once: the message is gone from the inbox, and the
	// other handle's message was untouched.
	assert.Empty(t, coord.drainLiveInboxFrom("parent-1", map[string]bool{run.handle: true}))
	assert.Equal(t, []SubagentInboxMessage{{AgentName: "task", Handle: other.handle, Text: "not for you"}}, coord.DrainSubagentInbox("parent-1"))
}

// TestWaitTool_WakesWhenRunFinishes covers the blocking path: wait parks on
// the inbox signal and returns as soon as the run finishes, without waiting
// out its timeout.
func TestWaitTool_WakesWhenRunFinishes(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")

	type result struct {
		resp fantasy.ToolResponse
		err  error
	}
	done := make(chan result, 1)
	sixty := 60
	go func() {
		tool := waitTool(t, coord)
		resp, err := tool.Run(sessionCtx(context.Background(), "parent-1"), waitInput(t, []string{run.handle}, &sixty))
		done <- result{resp, err}
	}()

	// The wait must be parked, not spinning: give it a moment to reach the
	// select, then finish the run exactly as the background goroutine would.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if coord.liveInboxSignalChan("parent-1") != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("done waiting"))
	coord.notifySubagentInbox("parent-1")

	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.False(t, r.resp.IsError, r.resp.Content)
		assert.Contains(t, r.resp.Content, "All waited agents finished.")
		assert.Contains(t, r.resp.Content, "done waiting")
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after the run finished")
	}
}

// TestWaitTool_WakesWhenMessageArrives covers the other wakeup: a message
// recorded while wait is parked returns it early.
func TestWaitTool_WakesWhenMessageArrives(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")

	done := make(chan fantasy.ToolResponse, 1)
	sixty := 60
	go func() {
		done <- runWait(t, coord, context.Background(), "parent-1", waitInput(t, []string{run.handle}, &sixty))
	}()

	time.Sleep(50 * time.Millisecond)
	require.NoError(t, coord.recordLiveSubagentMessage(run, "mid-run update"))

	select {
	case resp := <-done:
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "Returned early")
		assert.Contains(t, resp.Content, "mid-run update")
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after a message arrived")
	}
}

func TestWaitTool_UnknownHandle(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	known := registerTestRun(coord, "parent-1", "fast")

	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{"bg-nope"}, nil))
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "unknown background agent handle")
	assert.Contains(t, resp.Content, "bg-nope")
	assert.Contains(t, resp.Content, known.handle, "error should list the session's known handles")
}

func TestWaitTool_HandleScopedToDispatchingSession(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	registerTestRun(coord, "other-session", "fast")

	resp := runWait(t, coord, t.Context(), "mine", waitInput(t, []string{"bg-anything"}, nil))
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "unknown background agent handle")
	assert.NotContains(t, resp.Content, "other-session")
}

func TestWaitTool_Rejections(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)

	t.Run("no session in context", func(t *testing.T) {
		tool := waitTool(t, coord)
		resp, err := tool.Run(t.Context(), waitInput(t, []string{"bg-x"}, nil))
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "session id missing from context")
	})

	t.Run("malformed input", func(t *testing.T) {
		resp := runWait(t, coord, t.Context(), "parent-1", fantasy.ToolCall{Input: `{"handles":`})
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "invalid parameters")
	})

	t.Run("no handles and nothing dispatched", func(t *testing.T) {
		resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, nil, nil))
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "nothing to wait for")
	})
}

// TestWaitTool_MixedHandles renders a finished and a running handle in one
// response. Timeout 0 takes a snapshot instead of blocking on the running
// handle.
func TestWaitTool_MixedHandles(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	done := registerTestRun(coord, "parent-1", "fast")
	done.finish(subagents.StatusCompleted, fantasy.NewTextResponse("partial answer"))
	running := registerTestRun(coord, "parent-1", "task")

	zero := 0
	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{running.handle, done.handle}, &zero))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Snapshot")
	assert.Contains(t, resp.Content, "still running")
	assert.Contains(t, resp.Content, "partial answer")
}

// providerCfgP is shared by the wait tool tests.
var providerCfgP = config.ProviderConfig{ID: "p"}
