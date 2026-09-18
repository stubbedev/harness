package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedAgentTool blocks inside Run until released, so a test can queue a
// prompt mid-tool-loop, exactly where a user interjection lands while a
// long-running tool holds the turn.
type gatedAgentTool struct {
	entered chan struct{}
	release chan struct{}
}

func (g *gatedAgentTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: "gated"}
}

func (g *gatedAgentTool) Run(_ context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	close(g.entered)
	<-g.release
	return fantasy.NewTextResponse("gated tool done"), nil
}

func (g *gatedAgentTool) ProviderOptions() fantasy.ProviderOptions     { return nil }
func (g *gatedAgentTool) SetProviderOptions(_ fantasy.ProviderOptions) {}

// TestRun_QueuedPromptFoldsIntoNextRequestMidToolLoop pins the core
// interjection contract: a prompt queued while a tool call is executing
// joins the very next provider request as a user message - before the
// model decides anything else, never after a decision made on stale
// context.
func TestRun_QueuedPromptFoldsIntoNextRequestMidToolLoop(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	gated := &gatedAgentTool{entered: make(chan struct{}), release: make(chan struct{})}
	model := newScriptedModel(
		scriptedTurn{calls: []scriptedCall{{name: "gated"}}},
		scriptedTurn{text: "acknowledged"},
	)
	sa := testSessionAgent(env, model, textModel("title"), "system", gated).(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		_, runErr := sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "start"})
		runDone <- runErr
	}()

	select {
	case <-gated.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool call never started")
	}

	// The user interjects while the tool is blocked mid-loop.
	sa.enqueueCall(SessionAgentCall{SessionID: sess.ID, Prompt: "STEER: use plan b", acceptSeq: 1})
	close(gated.release)

	select {
	case runErr := <-runDone:
		require.NoError(t, runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never finished")
	}

	require.GreaterOrEqual(t, len(model.sentCalls()), 2,
		"the model must be called again after the tool result")
	found := false
	for _, msg := range model.sentCalls()[1].Prompt {
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && strings.Contains(text.Text, "STEER: use plan b") {
				found = true
			}
		}
	}
	require.True(t, found,
		"the queued prompt must be folded into the next provider request as a user message")
	require.Equal(t, 0, sa.QueuedPrompts(sess.ID), "the queue must drain into the fold")
}

// TestWaitTool_WakesWhenUserPromptQueued: a prompt queued while the wait
// tool is parked returns it early, citing the user - instead of sitting
// to its timeout - and leaves the prompt queued for the next step's
// fold, which still owns delivery.
func TestWaitTool_WakesWhenUserPromptQueued(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")

	done := make(chan fantasy.ToolResponse, 1)
	sixty := 60
	go func() {
		done <- runWait(t, coord, context.Background(), "parent-1", waitInput(t, []string{run.handle}, &sixty))
	}()

	time.Sleep(50 * time.Millisecond)
	coord.NotifyQueueArrival("parent-1")

	select {
	case resp := <-done:
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "Returned early")
		assert.Contains(t, resp.Content, "1 user prompt")
		assert.Contains(t, resp.Content, "still running",
			"the waited handle is unaffected; only the arrival is reported")
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after a user prompt was queued")
	}
}

// TestWaitTool_IgnoresPromptsQueuedBeforeWait: the baseline is taken at
// wait entry, so prompts already queued before the wait do not return
// it - only arrivals during the wait do.
func TestWaitTool_IgnoresPromptsQueuedBeforeWait(t *testing.T) {
	coord := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	run := registerTestRun(coord, "parent-1", "fast")

	coord.NotifyQueueArrival("parent-1")

	done := make(chan fantasy.ToolResponse, 1)
	one := 1
	go func() {
		done <- runWait(t, coord, context.Background(), "parent-1", waitInput(t, []string{run.handle}, &one))
	}()

	select {
	case resp := <-done:
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "Timed out after 1s",
			"an arrival before the wait must not end it")
	case <-time.After(5 * time.Second):
		t.Fatal("wait never returned")
	}
}

// TestHookedTool_NotesQueueArrivalDuringRun: a tool that cannot be
// interrupted still tells the model a user message arrived while it ran,
// so the model reads it on the next step instead of continuing blind.
func TestHookedTool_NotesQueueArrivalDuringRun(t *testing.T) {
	t.Parallel()

	signals := newQueueArrivalSignals()
	inner := &gatedAgentTool{entered: make(chan struct{}), release: make(chan struct{})}
	tool := newHookedTool(inner, nil, signals.epoch)

	runDone := make(chan fantasy.ToolResponse, 1)
	go func() {
		resp, err := tool.Run(sessionCtx(t.Context(), "s1"), fantasy.ToolCall{ID: "c1", Name: "gated"})
		require.NoError(t, err)
		runDone <- resp
	}()

	<-inner.entered
	signals.notify("s1")
	close(inner.release)

	select {
	case resp := <-runDone:
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "gated tool done")
		assert.Contains(t, resp.Content, "1 user message(s) arrived while this tool ran")
	case <-time.After(5 * time.Second):
		t.Fatal("the wrapped tool never returned")
	}
}
