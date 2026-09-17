package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/subagents"
)

// backgroundHandleFromResponse extracts the handle from a background
// dispatch response ("... with handle bg-xxxx ...").
func backgroundHandleFromResponse(t *testing.T, resp fantasy.ToolResponse) string {
	t.Helper()
	_, after, found := strings.Cut(resp.Content, "with handle ")
	require.True(t, found, "response should carry a handle: %s", resp.Content)
	handle, _, _ := strings.Cut(after, ".")
	require.NotEmpty(t, handle)
	require.True(t, strings.HasPrefix(handle, "bg-"), "handle should be bg-prefixed: %s", handle)
	return handle
}

func waitForRunFinished(t *testing.T, coord *coordinator, handle string) *backgroundRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if run, ok := coord.backgroundRuns.Get(handle); ok && run.isFinished() {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("background run did not finish in time")
	return nil
}

// TestRunSubAgentBackground_ReturnsImmediately covers the core contract:
// the dispatch returns a handle right away while the child is still running,
// and the child's result is stored against that handle when it finishes.
func TestRunSubAgentBackground_ReturnsImmediately(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})

	rt := subagents.NewRuntime()
	t.Cleanup(rt.Shutdown)
	coord.runtime = rt

	parentSession, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	released := make(chan struct{}, 1)
	releaseSlot := func() { released <- struct{}{} }

	childRunning := make(chan string, 1)
	agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		childRunning <- call.SessionID
		<-ctx.Done() // simulate a long-running child; cancelled at cleanup
		return nil, ctx.Err()
	})

	resp, err := coord.runSubAgent(t.Context(), subAgentParams{
		Agent:          agent,
		SessionID:      parentSession.ID,
		AgentMessageID: "msg-1",
		ToolCallID:     "call-1",
		Prompt:         "do something",
		SessionTitle:   "Test",
		AgentName:      "fast",
		Background:     true,
		ReleaseSlot:    releaseSlot,
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, `"fast"`)
	assert.Contains(t, resp.Content, AgentToolName)

	handle := backgroundHandleFromResponse(t, resp)
	childSession := <-childRunning

	// The run is registered and routed live while the child is still running.
	run, ok := coord.backgroundRunFor(parentSession.ID, handle)
	require.True(t, ok, "handle must resolve for its dispatching session")
	assert.Equal(t, childSession, run.childSession)
	assert.False(t, run.isFinished())
	assert.Len(t, rt.List(parentSession.ID), 1, "runtime must track the running child")

	// End the child run: cancel through the registry, as Cancel would.
	if cancel, ok := coord.subagentCancels.Get(childSession); ok {
		cancel()
	}

	finishedRun := waitForRunFinished(t, coord, handle)
	fin, status, result, isErr := finishedRun.snapshot()
	require.True(t, fin)
	assert.Equal(t, subagents.StatusCancelled, status)
	assert.True(t, isErr)
	assert.Contains(t, result, "cancelled")

	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch slot was not released when the run finished")
	}

	// The child's cancel-map entry is cleaned up once the run finishes.
	require.Eventually(t, func() bool {
		_, ok := coord.subagentCancels.Get(childSession)
		return !ok
	}, 5*time.Second, 5*time.Millisecond)
}

// TestRunSubAgentBackground_DetachedFromDispatchContext verifies the child
// survives the dispatching context being cancelled: a background agent
// outlives the tool call (and the turn) that started it.
func TestRunSubAgentBackground_DetachedFromDispatchContext(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	coord.runtime = subagents.NewRuntime()
	t.Cleanup(coord.runtime.Shutdown)

	parentSession, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	dispatchCtx, cancelDispatch := context.WithCancel(t.Context())
	childDone := make(chan struct{})

	agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		defer close(childDone)
		// The child's own context must outlive the dispatch context.
		<-ctx.Done()
		return agentResultWithText("late result"), nil
	})

	resp, err := coord.runSubAgent(dispatchCtx, subAgentParams{
		Agent:          agent,
		SessionID:      parentSession.ID,
		AgentMessageID: "msg-1",
		ToolCallID:     "call-1",
		Prompt:         "do something",
		SessionTitle:   "Test",
		AgentName:      "fast",
		Background:     true,
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	handle := backgroundHandleFromResponse(t, resp)

	// The dispatching turn ends here. The child keeps running.
	cancelDispatch()
	select {
	case <-childDone:
		t.Fatal("child must not be cancelled with the dispatch context")
	case <-time.After(150 * time.Millisecond):
	}

	// Now end it through its own registered cancel.
	if run, ok := coord.backgroundRuns.Get(handle); ok {
		if cancel, ok := coord.subagentCancels.Get(run.childSession); ok {
			cancel()
		}
	}
	select {
	case <-childDone:
	case <-time.After(5 * time.Second):
		t.Fatal("child was not cancelled by its own cancel func")
	}
	finished := waitForRunFinished(t, coord, handle)
	_, _, result, _ := finished.snapshot()
	assert.Contains(t, result, "late result")
}

// TestSendMessage_BackgroundChildDeliversLive covers the routing change: a
// background child's send_message lands in the dispatching session's live
// inbox (with its handle attached), not in the per-child completion inbox.
func TestSendMessage_BackgroundChildDeliversLive(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	coord.runtime = subagents.NewRuntime()
	t.Cleanup(coord.runtime.Shutdown)

	parentSession, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	childRunning := make(chan string, 1)
	agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		// Stand in for the child calling send_message mid-run.
		tool := &sendMessageTool{coord: coord}
		resp, err := tool.Run(sessionCtx(ctx, call.SessionID), fantasy.ToolCall{Input: `{"message":"live finding"}`})
		require.NoError(t, err)
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "next step")
		childRunning <- call.SessionID
		<-ctx.Done()
		return nil, ctx.Err()
	})

	resp, err := coord.runSubAgent(t.Context(), subAgentParams{
		Agent:          agent,
		SessionID:      parentSession.ID,
		AgentMessageID: "msg-1",
		ToolCallID:     "call-1",
		Prompt:         "test",
		SessionTitle:   "Test",
		AgentName:      "fast",
		Background:     true,
	})
	require.NoError(t, err)
	handle := backgroundHandleFromResponse(t, resp)
	childSession := <-childRunning

	// Live delivery: keyed by the dispatching session, tagged with the handle.
	msgs := coord.DrainSubagentInbox(parentSession.ID)
	require.Len(t, msgs, 1)
	assert.Equal(t, "live finding", msgs[0].Text)
	assert.Equal(t, handle, msgs[0].Handle)
	assert.Equal(t, "fast", msgs[0].AgentName)
	assert.Empty(t, coord.drainSubagentMessages(childSession), "nothing may remain in the completion inbox")

	if cancel, ok := coord.subagentCancels.Get(childSession); ok {
		cancel()
	}
	waitForRunFinished(t, coord, handle)
}

// TestSendMessage_BlockingChildKeepsCompletionDelivery pins the existing
// behavior for blocking dispatches: messages wait in the per-child inbox and
// ride along with the dispatch result.
func TestSendMessage_BlockingChildKeepsCompletionDelivery(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})

	parentSession, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		tool := &sendMessageTool{coord: coord}
		resp, err := tool.Run(sessionCtx(ctx, call.SessionID), fantasy.ToolCall{Input: `{"message":"blocking note"}`})
		require.NoError(t, err)
		require.False(t, resp.IsError)
		assert.Contains(t, resp.Content, "when your run completes")
		return agentResultWithText("done"), nil
	})

	resp, err := coord.runSubAgent(t.Context(), subAgentParams{
		Agent:          agent,
		SessionID:      parentSession.ID,
		AgentMessageID: "msg-1",
		ToolCallID:     "call-1",
		Prompt:         "test",
		SessionTitle:   "Test",
		AgentName:      "fast",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "blocking note")
	assert.Empty(t, coord.DrainSubagentInbox(parentSession.ID), "blocking dispatch must not touch the live inbox")
}

// TestLiveInbox_CapBackpressure verifies the loop-safety cap: past
// maxLiveInboxMessages, recording fails and send_message surfaces the error
// to the child.
func TestLiveInbox_CapBackpressure(t *testing.T) {
	t.Parallel()

	inbox := newLiveInbox()
	for i := range maxLiveInboxMessages {
		require.NoError(t, inbox.record("parent", SubagentInboxMessage{Handle: "bg-x", Text: strings.Repeat("m", i)}))
	}
	err := inbox.record("parent", SubagentInboxMessage{Handle: "bg-x", Text: "one too many"})
	require.ErrorContains(t, err, "not reading them")

	// Draining frees capacity again.
	require.Len(t, inbox.drain("parent"), maxLiveInboxMessages)
	require.NoError(t, inbox.record("parent", SubagentInboxMessage{Handle: "bg-x", Text: "ok"}))

	// The cap is per dispatching session.
	require.NoError(t, inbox.record("other-parent", SubagentInboxMessage{Text: "fine"}))
}

func TestLiveInbox_DrainFromLeavesOtherHandles(t *testing.T) {
	t.Parallel()

	inbox := newLiveInbox()
	require.NoError(t, inbox.record("parent", SubagentInboxMessage{Handle: "bg-a", Text: "a1"}))
	require.NoError(t, inbox.record("parent", SubagentInboxMessage{Handle: "bg-b", Text: "b1"}))
	require.NoError(t, inbox.record("parent", SubagentInboxMessage{Handle: "bg-a", Text: "a2"}))

	taken := inbox.drainFrom("parent", map[string]bool{"bg-a": true})
	require.Len(t, taken, 2)
	assert.Equal(t, "a1", taken[0].Text)
	assert.Equal(t, "a2", taken[1].Text)

	rest := inbox.drain("parent")
	require.Len(t, rest, 1)
	assert.Equal(t, "b1", rest[0].Text)
}

// TestRunSubAgentBackground_ForwardsStrandedMessages covers the belt-and-
// braces path: a message that nonetheless landed in the completion inbox is
// forwarded to the live inbox at finish rather than dropped.
func TestRunSubAgentBackground_ForwardsStrandedMessages(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	coord.runtime = subagents.NewRuntime()
	t.Cleanup(coord.runtime.Shutdown)

	parentSession, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	childSession := make(chan string, 1)
	agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		// Simulate a routing race: the message misses the live path and
		// lands where blocking dispatches deliver.
		coord.subagentMessages.Set(call.SessionID, []string{"straggler"})
		childSession <- call.SessionID
		return agentResultWithText("done"), nil
	})

	resp, err := coord.runSubAgent(t.Context(), subAgentParams{
		Agent:          agent,
		SessionID:      parentSession.ID,
		AgentMessageID: "msg-1",
		ToolCallID:     "call-1",
		Prompt:         "test",
		SessionTitle:   "Test",
		AgentName:      "fast",
		Background:     true,
	})
	require.NoError(t, err)
	handle := backgroundHandleFromResponse(t, resp)
	<-childSession

	waitForRunFinished(t, coord, handle)
	msgs := coord.DrainSubagentInbox(parentSession.ID)
	require.Len(t, msgs, 1)
	assert.Equal(t, "straggler", msgs[0].Text)
}

// fakeSubagentInbox is a static SubagentInboxSource for sessionAgent tests.
type fakeSubagentInbox struct {
	mu    sync.Mutex
	msgs  []SubagentInboxMessage
	drain int
}

func (f *fakeSubagentInbox) DrainSubagentInbox(_ string) []SubagentInboxMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs := f.msgs
	f.msgs = nil
	f.drain++
	return msgs
}

// TestPrepareStepFoldsLiveInboxMessages covers the step-loop half: a message
// a background child sent lands in the dispatcher's next step, as a persisted
// user message the model actually receives.
func TestPrepareStepFoldsLiveInboxMessages(t *testing.T) {
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	model := newScriptedModel(scriptedTurn{text: "ack"})
	// Titles go to the small model, and the title prompt quotes the user
	// prompt -- so sharing one model here made "the call mentioning
	// orchestrate" ambiguous, and which one came last depended on when a
	// background goroutine happened to finish. A separate title model
	// leaves the conversation as the only call on this one.
	inbox := &fakeSubagentInbox{msgs: []SubagentInboxMessage{{AgentName: "fast", Handle: "bg-abc", Text: "found the bug"}}}

	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel:    Model{Model: model},
		SmallModel:    Model{Model: textModel("title")},
		SystemPrompt:  "test",
		Sessions:      env.sessions,
		Messages:      env.messages,
		SubagentInbox: inbox,
	})

	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "orchestrate"})
	require.NoError(t, err)

	calls := model.sentCalls()
	require.Len(t, calls, 1, "the conversation is the only call on the large model")
	data, err := json.Marshal(calls[0].Prompt)
	require.NoError(t, err)
	conversation := string(data)
	require.Contains(t, conversation, "orchestrate")
	assert.Contains(t, conversation, "found the bug")
	assert.Contains(t, conversation, "Message from background agent")
	assert.Contains(t, conversation, "handle bg-abc")

	// The fold is persisted to the transcript as a user message carrying
	// a SubagentNote part: the model reads it as text, the transcript
	// never renders it.
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var found bool
	for _, m := range msgs {
		if m.Role != message.User {
			continue
		}
		for _, note := range m.SubagentNotes() {
			if note.AgentName == "fast" && note.Handle == "bg-abc" && strings.Contains(note.Text, "found the bug") {
				found = true
			}
		}
	}
	assert.True(t, found, "folded inbox message must appear in the transcript")

	// Drain semantics: the same message is never folded twice.
	require.Equal(t, 1, inbox.drain)
}

// TestRunSubAgentBackground_SessionCreateFailureReleasesSlot pins the slot
// ownership rules: a background dispatch that fails before the run starts
// must give the slot back.
func TestRunSubAgentBackground_SessionCreateFailureReleasesSlot(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	coord.runtime = subagents.NewRuntime()
	t.Cleanup(coord.runtime.Shutdown)

	parentSession, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	// Pre-create the child session ID runSubAgent will try to use so
	// CreateTaskSession fails on the duplicate primary key.
	agentToolSessionID := coord.sessions.CreateAgentToolSessionID("msg-dup", "call-dup")
	_, err = env.sessions.CreateTaskSession(t.Context(), agentToolSessionID, parentSession.ID, "Dup")
	require.NoError(t, err)

	released := make(chan struct{}, 1)
	_, err = coord.runSubAgent(t.Context(), subAgentParams{
		Agent:          newMockAgent(providerID, 4096, nil),
		SessionID:      parentSession.ID,
		AgentMessageID: "msg-dup",
		ToolCallID:     "call-dup",
		Prompt:         "test",
		SessionTitle:   "Test",
		Background:     true,
		ReleaseSlot:    func() { released <- struct{}{} },
	})
	require.NoError(t, err)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("slot was not released when session creation failed")
	}
}

// TestBackgroundRun_FinishIsIdempotent guards the record against duplicate
// finish calls overwriting the collected result.
func TestBackgroundRun_FinishIsIdempotent(t *testing.T) {
	t.Parallel()

	run := &backgroundRun{handle: "bg-x", parentSession: "p", agentName: "fast"}
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("first"))
	run.finish(subagents.StatusFailed, fantasy.NewTextErrorResponse("second"))

	finished, status, result, isErr := run.snapshot()
	require.True(t, finished)
	assert.Equal(t, subagents.StatusCompleted, status)
	assert.Equal(t, "first", result)
	assert.False(t, isErr)
}

var (
	_ SubagentInboxSource = (*fakeSubagentInbox)(nil)
	_ SubagentInboxSource = (*coordinator)(nil)
)
