package agent

import (
	"context"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/subagents"
)

const controlTestProvider = "test-provider"

type controlFixture struct {
	coord  *coordinator
	env    fakeEnv
	parent string
}

func newControlFixture(t *testing.T) controlFixture {
	t.Helper()
	env := testEnv(t)
	coord := newTestCoordinator(t, env, controlTestProvider, config.ProviderConfig{ID: controlTestProvider})
	coord.runtime = subagents.NewRuntime()
	t.Cleanup(coord.runtime.Shutdown)
	parent, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)
	return controlFixture{coord: coord, env: env, parent: parent.ID}
}

type backgroundChild struct {
	handle  string
	session string
	finish  chan struct{}
}

func (f controlFixture) startChild(t *testing.T, name string) backgroundChild {
	t.Helper()
	running := make(chan string, 1)
	finish := make(chan struct{})
	agent := newMockAgent(controlTestProvider, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		running <- call.SessionID
		select {
		case <-finish:
			return agentResultWithText(name + " done"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	resp, err := f.coord.runSubAgent(t.Context(), subAgentParams{
		Agent:          agent,
		SessionID:      f.parent,
		AgentMessageID: "msg-" + name,
		ToolCallID:     "call-" + name,
		Prompt:         "work",
		SessionTitle:   name,
		AgentName:      name,
		Background:     true,
	})
	require.NoError(t, err)
	child := backgroundChild{handle: backgroundHandleFromResponse(t, resp), session: <-running, finish: finish}
	t.Cleanup(func() {
		f.coord.subagentRuns.cancel(child.session)
	})
	return child
}

func (f controlFixture) finishChild(t *testing.T, child backgroundChild) *backgroundRun {
	t.Helper()
	close(child.finish)
	return waitForRunFinished(t, f.coord, f.parent, child.handle)
}

func steeringTexts(msgs []SubagentInboxMessage) []string {
	texts := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Steering != nil {
			texts = append(texts, msg.Text)
		}
	}
	return texts
}

func userTexts(t *testing.T, f controlFixture, sessionID string) []string {
	t.Helper()
	msgs, err := f.env.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	var texts []string
	for _, msg := range msgs {
		if msg.Role == message.User {
			texts = append(texts, msg.Content().Text)
		}
	}
	return texts
}

func TestMessageSubagentsDeliversToNamedRunningAgent(t *testing.T) {
	f := newControlFixture(t)
	first := f.startChild(t, "first")
	second := f.startChild(t, "second")

	resp := f.coord.messageSubagents(t.Context(), f.parent, []string{first.handle}, "  skip the cargo tests  ")
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Message delivered to 1 agent(s): first ("+first.handle+")")

	assert.Equal(t, []string{"skip the cargo tests"}, steeringTexts(f.coord.DrainSubagentInbox(first.session)))
	assert.Empty(t, f.coord.DrainSubagentInbox(second.session), "an agent not named must not receive the message")
	assert.Contains(t, userTexts(t, f, first.session), "skip the cargo tests", "the message is persisted to the agent's transcript")
}

func TestMessageSubagentsBroadcastsToEveryRunningAgent(t *testing.T) {
	f := newControlFixture(t)
	running := f.startChild(t, "running")
	alsoRunning := f.startChild(t, "also")
	finished := f.startChild(t, "finished")
	f.finishChild(t, finished)

	resp := f.coord.messageSubagents(t.Context(), f.parent, nil, "commit only")
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Message delivered to 2 agent(s)")
	assert.Equal(t, []string{"commit only"}, steeringTexts(f.coord.DrainSubagentInbox(running.session)))
	assert.Equal(t, []string{"commit only"}, steeringTexts(f.coord.DrainSubagentInbox(alsoRunning.session)))
	assert.NotContains(t, userTexts(t, f, finished.session), "commit only")
}

func TestMessageSubagentsRejectsFinishedHandleWithoutSending(t *testing.T) {
	f := newControlFixture(t)
	running := f.startChild(t, "running")
	finished := f.startChild(t, "finished")
	f.finishChild(t, finished)

	resp := f.coord.messageSubagents(t.Context(), f.parent, []string{running.handle, finished.handle}, "stop testing")
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "already finished")
	assert.Contains(t, resp.Content, finished.handle)
	assert.Empty(t, f.coord.DrainSubagentInbox(running.session), "a rejected call sends to no one")
}

func TestMessageSubagentsRejectsUnknownHandle(t *testing.T) {
	f := newControlFixture(t)
	running := f.startChild(t, "running")

	resp := f.coord.messageSubagents(t.Context(), f.parent, []string{"bg-nope"}, "hello")
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "unknown background agent handle(s): bg-nope")
	assert.Contains(t, resp.Content, running.handle)
}

func TestMessageSubagentsWithNothingRunningIsAnError(t *testing.T) {
	f := newControlFixture(t)
	f.finishChild(t, f.startChild(t, "finished"))

	resp := f.coord.messageSubagents(t.Context(), f.parent, nil, "hello")
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "no running background agents")

	blank := f.coord.messageSubagents(t.Context(), f.parent, nil, "   ")
	require.True(t, blank.IsError)
}

func TestSteeringUnreadAtFinishIsReportedInResult(t *testing.T) {
	f := newControlFixture(t)
	child := f.startChild(t, "worker")

	resp := f.coord.messageSubagents(t.Context(), f.parent, []string{child.handle}, "late instruction")
	require.False(t, resp.IsError, resp.Content)

	run := f.finishChild(t, child)
	_, status, result, _ := run.snapshot()
	assert.Equal(t, subagents.StatusCompleted, status)
	assert.Contains(t, result, "worker done")
	assert.Contains(t, result, "after this agent's last step (not read)")
	assert.Contains(t, result, "late instruction")
}

func TestSteerSubagentAfterFinishFailsAndPersistsNothing(t *testing.T) {
	f := newControlFixture(t)
	child := f.startChild(t, "worker")
	f.finishChild(t, child)

	err := f.coord.SteerSubagent(t.Context(), child.session, "too late")
	require.ErrorIs(t, err, errSubagentNotRunning)
	assert.NotContains(t, userTexts(t, f, child.session), "too late")
}

func TestLiveInboxRefusesSteeringOnceClosed(t *testing.T) {
	t.Parallel()
	inbox := newLiveInbox()
	inbox.openSteering("child")
	require.NoError(t, inbox.recordSteering("child", SubagentInboxMessage{Text: "first"}))

	unread := inbox.closeSteering("child")
	require.Len(t, unread, 1)
	assert.Equal(t, "first", unread[0].Text)

	require.ErrorIs(t, inbox.recordSteering("child", SubagentInboxMessage{Text: "second"}), errSubagentNotRunning)
	assert.Empty(t, inbox.drain("child"))
}

func TestCancelSubagentsCancelsNamedRunningAgent(t *testing.T) {
	f := newControlFixture(t)
	target := f.startChild(t, "target")
	bystander := f.startChild(t, "bystander")

	resp := f.coord.cancelSubagents(f.parent, []string{target.handle})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Cancelled 1 agent(s): target ("+target.handle+")")

	_, status, _, _ := waitForRunFinished(t, f.coord, f.parent, target.handle).snapshot()
	assert.Equal(t, subagents.StatusCancelled, status)
	run, ok := f.coord.subagentRuns.lookupByHandle(f.parent, bystander.handle)
	require.True(t, ok)
	assert.False(t, run.isFinished(), "only the named agent is cancelled")
}

func TestCancelSubagentsRequiresRunningHandles(t *testing.T) {
	f := newControlFixture(t)
	finished := f.startChild(t, "finished")
	f.finishChild(t, finished)

	noHandles := f.coord.cancelSubagents(f.parent, nil)
	require.True(t, noHandles.IsError)
	assert.Contains(t, noHandles.Content, `"handles"`)

	done := f.coord.cancelSubagents(f.parent, []string{finished.handle})
	require.True(t, done.IsError)
	assert.Contains(t, done.Content, "already finished")
}

// TestDiscardSubagentDispatchCancelsAndForgetsRun pins what happens to a
// background dispatch that started early in a step the stream then
// discarded: its run is cancelled, and no wait or handle list mentions
// it, since the model never saw its handle. Other runs are untouched.
func TestDiscardSubagentDispatchCancelsAndForgetsRun(t *testing.T) {
	f := newControlFixture(t)
	discarded := f.startChild(t, "discarded")
	kept := f.startChild(t, "kept")
	run, ok := f.coord.subagentRuns.lookupByHandle(f.parent, discarded.handle)
	require.True(t, ok)

	f.coord.DiscardSubagentDispatch(discarded.session)

	assert.NotContains(t, f.coord.subagentRuns.handles(f.parent), discarded.handle)
	assert.Contains(t, f.coord.subagentRuns.handles(f.parent), kept.handle)
	require.Eventually(t, run.isFinished, 5*time.Second, 10*time.Millisecond, "the discarded run is cancelled")
	_, status, _, _ := run.snapshot()
	assert.Equal(t, subagents.StatusCancelled, status)
	keptRun, ok := f.coord.subagentRuns.lookupByHandle(f.parent, kept.handle)
	require.True(t, ok)
	assert.False(t, keptRun.isFinished())
}

func TestAgentDispatchParamsKind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		input    string
		finished bool
		want     AgentCallKind
		decided  bool
		control  bool
	}{
		{name: "dispatch", input: `{"prompt":"go"}`, finished: true, want: AgentCallDispatch, decided: true},
		{name: "wait", input: `{"handles":["bg-1"]}`, finished: true, want: AgentCallWait, decided: true, control: true},
		{name: "empty wait", input: `{}`, finished: true, want: AgentCallWait, decided: true, control: true},
		{name: "message", input: `{"message":"hi","handles":["bg-1"]}`, finished: true, want: AgentCallMessage, decided: true, control: true},
		{name: "cancel", input: `{"cancel":true,"handles":["bg-1"]}`, finished: true, want: AgentCallCancel, decided: true, control: true},
		{name: "streaming nothing yet", input: ``, want: AgentCallWait},
		{name: "streaming open brace", input: `{`, want: AgentCallWait},
		{name: "streaming handles", input: `{"handles":["bg-1"`, want: AgentCallWait},
		{name: "streaming prompt", input: `{"prompt":"Audit the UI`, want: AgentCallDispatch, decided: true},
		{name: "streaming message", input: `{"handles":["bg-1"],"message":"sto`, want: AgentCallMessage, decided: true, control: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := message.ToolCall{Name: AgentToolName, Input: tc.input, Finished: tc.finished}
			_, kind, decided := DecodeAgentCall(call)
			assert.Equal(t, tc.want, kind)
			assert.Equal(t, tc.decided, decided)
			assert.Equal(t, tc.control, IsAgentControlCall(call))
		})
	}
	assert.False(t, IsAgentControlCall(message.ToolCall{Name: "research", Input: `{}`, Finished: true}), "only the agent tool has control forms")
}

// TestDecodeAgentCallReadsStreamingPrompt pins that a dispatch still
// streaming its input exposes the prompt typed so far, so the task strip
// can show the agent being prepared instead of a wait.
func TestDecodeAgentCallReadsStreamingPrompt(t *testing.T) {
	t.Parallel()
	params, kind, decided := DecodeAgentCall(message.ToolCall{Name: AgentToolName, Input: `{"blocking":true,"prompt":"Audit the UI and`})
	assert.True(t, decided)
	assert.Equal(t, AgentCallDispatch, kind)
	assert.Equal(t, "Audit the UI and", params.Prompt)
	assert.True(t, params.Blocking)
}

func TestAgentDispatchParamsRejectsMixedForms(t *testing.T) {
	t.Parallel()
	require.NoError(t, AgentDispatchParams{Message: "hi"}.validateForm())
	require.Error(t, AgentDispatchParams{Prompt: "go", Message: "hi"}.validateForm())
	require.Error(t, AgentDispatchParams{Prompt: "go", Cancel: true}.validateForm())
	require.Error(t, AgentDispatchParams{Message: "hi", Cancel: true}.validateForm())
}
