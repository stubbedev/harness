package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/subagents"
)

// providerCfgP is shared by the wait tool tests.
var providerCfgP = config.ProviderConfig{ID: "p"}

// registerTestRun puts a background run on the coordinator the way a real
// background dispatch would, without running a child.
func registerTestRun(coord *coordinator, parentSession, name string) *backgroundRun {
	run, _ := coord.subagentRuns.registerBackground(parentSession, "child-"+parentSession+"-"+name, name, func() {})
	run.markStarted()
	return run
}

// waitInput is a wait tool call naming the given ids. A nil slice leaves
// wait_for out entirely, which waits on everything outstanding.
func waitInput(t *testing.T, waitFor []string, timeoutSeconds *int) fantasy.ToolCall {
	t.Helper()
	input := map[string]any{}
	if waitFor != nil {
		input["wait_for"] = waitFor
	}
	if timeoutSeconds != nil {
		input["timeout_seconds"] = *timeoutSeconds
	}
	data, err := json.Marshal(input)
	require.NoError(t, err)
	return fantasy.ToolCall{Input: string(data)}
}

// waitToolFor is the wait tool the coordinator installs for every agent.
func waitToolFor(t *testing.T, coord *coordinator) fantasy.AgentTool {
	t.Helper()
	return tools.NewWaitTool(coord)
}

// runWait drives the wait tool as the agent loop would, with the session
// in the call's context.
func runWait(t *testing.T, coord *coordinator, ctx context.Context, sessionID string, call fantasy.ToolCall) fantasy.ToolResponse {
	t.Helper()
	resp, err := waitToolFor(t, coord).Run(sessionCtx(ctx, sessionID), call)
	require.NoError(t, err)
	return resp
}

// startTestJob starts a real background job of the session, returning its
// id, and stops it when the test ends so a sleeping command cannot leak.
func startTestJob(t *testing.T, coord *coordinator, sessionID, command string) string {
	t.Helper()
	job, err := coord.jobs.start(sessionID, command, t.TempDir(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { coord.jobs.cancel(job.parentSession, job.id) })
	return job.id
}

// armTestTimer arms a timer of the session against the coordinator's
// clock, which the caller may replace with a fake one.
func armTestTimer(t *testing.T, coord *coordinator, sessionID string, d time.Duration) string {
	t.Helper()
	timer, err := coord.timers.arm(sessionID, d)
	require.NoError(t, err)
	return timer.id
}

func TestWaitTool_FinishedRunReturnsResult(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("the answer"))

	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{run.handle}, nil))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Every waited piece of work finished")
	assert.Contains(t, resp.Content, "fast ("+run.handle+"): completed")
	assert.Contains(t, resp.Content, "the answer")

	resp2 := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{run.handle}, nil))
	require.False(t, resp2.IsError, resp2.Content)
	assert.Contains(t, resp2.Content, "result already returned by an earlier wait")
	assert.NotContains(t, resp2.Content, "the answer", "a collected result must not be returned twice")

	resp3 := runWait(t, coord, t.Context(), "parent-1", waitInput(t, nil, nil))
	require.True(t, resp3.IsError)
	assert.Contains(t, resp3.Content, "already returned")
}

// TestWaitTool_MixedKindsSnapshot names a job, a timer and an agent in one
// call: every kind is resolved and reported, and a 0-second timeout takes
// the snapshot without blocking.
func TestWaitTool_MixedKindsSnapshot(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	coord.timers.now = newFakeClock().Now
	job := startTestJob(t, coord, "parent-1", "sleep 60")
	timer := armTestTimer(t, coord, "parent-1", 30*time.Second)
	run := registerTestRun(coord, "parent-1", "fast")

	zero := 0
	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{job, timer, run.handle}, &zero))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Snapshot (timeout 0)")
	assert.Contains(t, resp.Content, "background job "+job+" (sleep 60): running for")
	assert.Contains(t, resp.Content, "timer "+timer+" (30s): armed; fires in 30s")
	assert.Contains(t, resp.Content, "fast ("+run.handle+"): still running")

	// Nothing was collected: a snapshot does not take a result.
	assert.False(t, coord.jobs.lookup("parent-1", job).isCollected())
	assert.False(t, coord.timers.lookup("parent-1", timer).isCollected())
	assert.False(t, run.isCollected())
}

// TestWaitTool_EmptyWaitForNamesEverythingOutstanding pins the empty call:
// it watches every uncollected result of every kind, and after collecting
// them there is nothing left to wait on.
func TestWaitTool_EmptyWaitForNamesEverythingOutstanding(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	coord.timers.now = newFakeClock().Now

	job := startTestJob(t, coord, "parent-1", "echo job-done")
	require.Eventually(t, func() bool {
		return coord.jobs.lookup("parent-1", job).info().State == JobExited
	}, 30*time.Second, 10*time.Millisecond)

	timer := armTestTimer(t, coord, "parent-1", 5*time.Second)
	require.True(t, coord.timers.lookup("parent-1", timer).fire())

	run := registerTestRun(coord, "parent-1", "fast")
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("agent-done"))

	zero := 0
	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, nil, &zero))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Every waited piece of work finished")
	assert.Contains(t, resp.Content, "background job "+job)
	assert.Contains(t, resp.Content, "job-done", "a finished job's output tail is reported")
	assert.Contains(t, resp.Content, "timer "+timer+" (5s): fired")
	assert.Contains(t, resp.Content, "agent-done")

	resp2 := runWait(t, coord, t.Context(), "parent-1", waitInput(t, nil, &zero))
	require.True(t, resp2.IsError)
	assert.Contains(t, resp2.Content, "nothing to wait for")
	assert.Contains(t, resp2.Content, "already returned")
}

// TestWaitTool_CollectsEachKindExactlyOnce runs two waits over the same
// three ids: the first hands back every result, the second only names what
// it already returned.
func TestWaitTool_CollectsEachKindExactlyOnce(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	coord.timers.now = newFakeClock().Now

	job := startTestJob(t, coord, "parent-1", "echo only-once")
	require.Eventually(t, func() bool {
		return coord.jobs.lookup("parent-1", job).info().State == JobExited
	}, 30*time.Second, 10*time.Millisecond)
	timer := armTestTimer(t, coord, "parent-1", 5*time.Second)
	require.True(t, coord.timers.lookup("parent-1", timer).fire())
	run := registerTestRun(coord, "parent-1", "fast")
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("answer-once"))

	ids := []string{job, timer, run.handle}
	zero := 0
	first := runWait(t, coord, t.Context(), "parent-1", waitInput(t, ids, &zero))
	require.False(t, first.IsError, first.Content)
	assert.Contains(t, first.Content, "only-once")
	assert.Contains(t, first.Content, "answer-once")

	second := runWait(t, coord, t.Context(), "parent-1", waitInput(t, ids, &zero))
	require.False(t, second.IsError, second.Content)
	assert.Contains(t, second.Content, "Nothing is outstanding")
	assert.NotContains(t, second.Content, "Output (tail)", "a collected job's output is not repeated")
	assert.NotContains(t, second.Content, "answer-once", "a collected agent result is not repeated")
	assert.Equal(t, 3, strings.Count(second.Content, "result already returned by an earlier wait"))
}

// TestWaitTool_ReportsJobExitCodeAndOutput covers a failing command: its
// non-zero status and the tail it printed before dying both land in the
// response.
func TestWaitTool_ReportsJobExitCodeAndOutput(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	job := startTestJob(t, coord, "parent-1", "echo about-to-fail >&2; exit 3")
	require.Eventually(t, func() bool {
		return coord.jobs.lookup("parent-1", job).info().State != JobRunning
	}, 30*time.Second, 10*time.Millisecond)

	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{job}, nil))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "background job "+job)
	assert.Contains(t, resp.Content, "failed, exit code 3")
	assert.Contains(t, resp.Content, "about-to-fail")
}

// TestWaitTool_TimerRemainingThenFired walks a timer from armed to fired on
// the registry's fake clock: the remaining time shrinks, then the fire is
// collectable.
func TestWaitTool_TimerRemainingThenFired(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	clock := newFakeClock()
	coord.timers.now = clock.Now
	coord.timers.after = clock.After
	timer := armTestTimer(t, coord, "parent-1", 30*time.Second)

	zero := 0
	armed := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{timer}, &zero))
	require.False(t, armed.IsError, armed.Content)
	assert.Contains(t, armed.Content, "timer "+timer+" (30s): armed; fires in 30s")
	assert.Contains(t, armed.Content, "Snapshot (timeout 0)")

	clock.Advance(10 * time.Second)
	midway := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{timer}, &zero))
	require.False(t, midway.IsError, midway.Content)
	assert.Contains(t, midway.Content, "fires in 20s")

	clock.Advance(20 * time.Second)
	require.Eventually(t, func() bool {
		return coord.timers.lookup("parent-1", timer).info(clock.Now()).State == TimerFired
	}, 5*time.Second, time.Millisecond)

	fired := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{timer}, &zero))
	require.False(t, fired.IsError, fired.Content)
	assert.Contains(t, fired.Content, "timer "+timer+" (30s): fired")
	assert.True(t, coord.timers.lookup("parent-1", timer).isCollected())

	again := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{timer}, &zero))
	require.False(t, again.IsError, again.Content)
	assert.Contains(t, again.Content, "result already returned by an earlier wait")
}

func TestWaitTool_UnknownIDFailsBeforeWaiting(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	job := startTestJob(t, coord, "parent-1", "sleep 60")

	resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, []string{"job-99", "bg-nope"}, nil))
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "unknown wait target(s): job-99, bg-nope")
	assert.Contains(t, resp.Content, "Ids this session owns: "+job)
	assert.False(t, coord.jobs.lookup("parent-1", job).isCollected(), "the call failed before waiting")
}

func TestWaitTool_IDsAreScopedToTheirSession(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	startTestJob(t, coord, "other-session", "sleep 60")

	resp := runWait(t, coord, t.Context(), "mine", waitInput(t, []string{"job-1"}, nil))
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "unknown wait target(s): job-1")
	assert.NotContains(t, resp.Content, "other-session")
}

// TestWaitTool_QueuedPromptReturnsEarly: a prompt queued while the wait
// runs interjects, and the response still says what is running.
func TestWaitTool_QueuedPromptReturnsEarly(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	job := startTestJob(t, coord, "parent-1", "sleep 60")

	done := make(chan fantasy.ToolResponse, 1)
	sixty := 60
	go func() {
		done <- runWait(t, coord, context.Background(), "parent-1", waitInput(t, []string{job}, &sixty))
	}()

	time.Sleep(50 * time.Millisecond)
	coord.NotifyQueueArrival("parent-1")

	select {
	case resp := <-done:
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "Returned early")
		assert.Contains(t, resp.Content, "1 user prompt")
		assert.Contains(t, resp.Content, "background job "+job)
		assert.Contains(t, resp.Content, "running for",
			"the snapshot names what is still outstanding")
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after a user prompt was queued")
	}
}

// TestWaitTool_CancelledContextReturnsEarly: a cancelled turn ends the
// wait with what is still running instead of sleeping out the timeout.
func TestWaitTool_CancelledContextReturnsEarly(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan fantasy.ToolResponse, 1)
	sixty := 60
	go func() {
		done <- runWait(t, coord, ctx, "parent-1", waitInput(t, []string{run.handle}, &sixty))
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case resp := <-done:
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "Interrupted")
		assert.Contains(t, resp.Content, "fast ("+run.handle+"): still running")
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after its context was cancelled")
	}
}

func TestWaitTool_ReturnsWhenAnyWaitedTargetFinishes(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	first, err := coord.jobs.start("parent-1", "echo first-job", t.TempDir(), t.TempDir())
	require.NoError(t, err)
	slow := registerTestRun(coord, "parent-1", "task")

	done := make(chan fantasy.ToolResponse, 1)
	sixty := 60
	go func() {
		done <- runWait(t, coord, context.Background(), "parent-1", waitInput(t, []string{first.id, slow.handle}, &sixty))
	}()

	require.Eventually(t, func() bool {
		return first.info().State != JobRunning
	}, 30*time.Second, 5*time.Millisecond)

	select {
	case resp := <-done:
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "1 finished and collected; 1 still outstanding")
		assert.Contains(t, resp.Content, "first-job")
		assert.Contains(t, resp.Content, "task ("+slow.handle+"): still running")
	case <-time.After(5 * time.Second):
		t.Fatal("wait must return when one of the waited targets finishes")
	}
}

func TestWaitTool_WakesWhenRunFinishes(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")

	done := make(chan fantasy.ToolResponse, 1)
	sixty := 60
	go func() {
		done <- runWait(t, coord, context.Background(), "parent-1", waitInput(t, []string{run.handle}, &sixty))
	}()

	time.Sleep(50 * time.Millisecond)
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("done waiting"))
	coord.notifySubagentInbox("parent-1")

	select {
	case resp := <-done:
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "Every waited piece of work finished")
		assert.Contains(t, resp.Content, "done waiting")
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after the run finished")
	}
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
	assert.Equal(t, []SubagentInboxMessage{{AgentName: "task", Handle: other.handle, ChildSessionID: other.childSession, Text: "not for you"}}, coord.DrainSubagentInbox("parent-1"))
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

func TestWaitTool_ConcurrentWaitsCollectOnce(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("only once"))

	results := make(chan fantasy.ToolResponse, 4)
	for range 4 {
		go func() {
			results <- runWait(t, coord, context.Background(), "parent-1", waitInput(t, []string{run.handle}, nil))
		}()
	}
	delivered := 0
	for range 4 {
		resp := <-results
		require.False(t, resp.IsError, resp.Content)
		if strings.Contains(resp.Content, "only once") {
			delivered++
		}
	}
	assert.Equal(t, 1, delivered)
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

func TestWaitTool_Rejections(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)

	t.Run("no session in context", func(t *testing.T) {
		resp, err := waitToolFor(t, coord).Run(t.Context(), waitInput(t, []string{"job-1"}, nil))
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "session ID is required")
	})

	t.Run("malformed input", func(t *testing.T) {
		resp := runWait(t, coord, t.Context(), "parent-1", fantasy.ToolCall{Input: `{"wait_for":`})
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "invalid parameters")
	})

	t.Run("nothing outstanding", func(t *testing.T) {
		resp := runWait(t, coord, t.Context(), "parent-1", waitInput(t, nil, nil))
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "nothing to wait for")
		assert.Contains(t, resp.Content, "no background jobs, timers or agents")
	})
}

// TestWaitTool_ScriptedTurnWaitsOnceForAJob is the point of the feature: a
// turn starts a background job, the next turn waits, and the wait wakes
// when the job finishes instead of polling. The assertion is on the model
// turns - one to dispatch, one to wait, one to answer - not on the clock.
func TestWaitTool_ScriptedTurnWaitsOnceForAJob(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	coord := newTestCoordinator(t, env, "p", providerCfgP)
	require.True(t, tools.ShellAvailable(), "the test needs a shell to start a job")

	large := newScriptedModel(
		scriptedTurn{calls: []scriptedCall{{
			name: "shell",
			input: map[string]any{
				"command":           "sleep 0.2; echo job-done",
				"run_in_background": true,
			},
		}}},
		scriptedTurn{calls: []scriptedCall{{name: "wait", input: map[string]any{}}}},
		scriptedTurn{text: "the job finished"},
	)
	shell := tools.NewShellTool(env.workingDir, "coder", nil, *env.filetracker, coord)
	agent := testSessionAgent(env, large, textModel("A Session"), "system", shell, tools.NewWaitTool(coord))

	msgs := runScript(t, agent, env, "run the tests in the background, then wait for them")

	waits := 0
	for _, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}
		for _, call := range msg.ToolCalls() {
			if call.Name == "wait" {
				waits++
			}
		}
	}
	assert.Equal(t, 1, waits, "the wait is called once and wakes on the job's finish; it never polls")
	assert.Len(t, large.sentCalls(), 3, "dispatch, wait, answer: no polling turn in between")

	results := toolResults(t, msgs)
	require.Contains(t, results, "wait")
	assert.False(t, results["wait"].IsError, results["wait"].Content)
	assert.Contains(t, results["wait"].Content, "Every waited piece of work finished")
	assert.Contains(t, results["wait"].Content, "job-done", "the job's output tail comes back with its result")
}
