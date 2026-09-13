package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

// cancelStreamModel streams normally on every attempt after the first.
// The first Stream call blocks until its context is canceled and then
// returns the cancellation error, which is how a provider stream cut off
// by CancelTurn surfaces inside Run.
type cancelStreamModel struct {
	attempts atomic.Int32
}

func (m *cancelStreamModel) Provider() string { return "fake" }
func (m *cancelStreamModel) Model() string    { return "fake-model" }

func (m *cancelStreamModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: "done"}},
		FinishReason: fantasy.FinishReasonStop,
	}, nil
}

func (m *cancelStreamModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	if m.attempts.Add(1) == 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	text := "done"
	return func(yield func(fantasy.StreamPart) bool) {
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "1"}) {
			return
		}
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "1", Delta: text}) {
			return
		}
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "1"}) {
			return
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

func (m *cancelStreamModel) GenerateObject(ctx context.Context, call fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, context.DeadlineExceeded
}

func (m *cancelStreamModel) StreamObject(ctx context.Context, call fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, context.DeadlineExceeded
}

// TestCancelTurn_KeepsQueueAndAcceptedRuns pins the distinction between
// the two cancel variants: CancelTurn interrupts the active request only,
// leaving queued prompts and accepted runs untouched, while Cancel drops
// pending work.
func TestCancelTurn_KeepsQueueAndAcceptedRuns(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	model := &cancelStreamModel{}
	sa := testSessionAgent(env, model, model, "system").(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	var activeCanceled atomic.Bool
	sa.activeRequests.Set(sess.ID, &activeCancel{cancel: func() { activeCanceled.Store(true) }})
	sa.enqueueCall(SessionAgentCall{SessionID: sess.ID, Prompt: "queued-followup", acceptSeq: 1})
	accept := sa.BeginAccepted(sess.ID)
	defer accept.Close()

	sa.CancelTurn(sess.ID)

	require.True(t, activeCanceled.Load(), "the active cancel func must fire")
	require.Equal(t, 1, sa.QueuedPrompts(sess.ID), "a turn-only cancel must keep the queue")
	require.False(t, sa.hasPendingCancel(sess.ID), "a turn-only cancel must not poison accepted runs")

	// The drop-everything variant clears the queue for contrast.
	sa.Cancel(sess.ID)
	require.Equal(t, 0, sa.QueuedPrompts(sess.ID), "a full cancel drops the queue")
}

// TestRun_CanceledTurnRunsQueuedFollowUp is the interrupt-and-steer
// contract: canceling the active turn must not strand prompts queued
// behind it. The follow-up runs as its own turn once the canceled turn
// unwinds.
func TestRun_CanceledTurnRunsQueuedFollowUp(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	model := &cancelStreamModel{}
	// The title generator runs concurrently against the small model;
	// give it its own model so it cannot consume the large model's
	// blocking attempt.
	sa := testSessionAgent(env, model, &finishStreamModel{text: "title"}, "system").(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		_, runErr := sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "main"})
		runDone <- runErr
	}()

	// Wait for the run to register itself, then queue a follow-up behind
	// it the way a prompt submitted mid-turn is, and cancel the turn.
	require.Eventually(t, func() bool {
		select {
		case runErr := <-runDone:
			t.Fatalf("Run returned before becoming active: %v", runErr)
			return false
		default:
		}
		return sa.IsSessionBusy(sess.ID)
	}, 5*time.Second, 10*time.Millisecond, "the main turn must become active")
	sa.enqueueCall(SessionAgentCall{SessionID: sess.ID, Prompt: "next", acceptSeq: 1})
	sa.CancelTurn(sess.ID)

	select {
	case runErr := <-runDone:
		require.NoError(t, runErr, "the follow-up turn must complete normally")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the follow-up turn")
	}

	require.Equal(t, 0, sa.QueuedPrompts(sess.ID), "the follow-up must leave the queue")
	require.False(t, sa.hasPendingCancel(sess.ID))

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 4, "main turn (user + canceled assistant) then the follow-up turn")
	assert.Equal(t, message.User, msgs[0].Role)
	assert.Equal(t, "main", msgs[0].Content().String())
	assert.Equal(t, message.Assistant, msgs[1].Role)
	assert.Equal(t, message.FinishReasonCanceled, msgs[1].FinishReason(),
		"the interrupted turn is marked canceled")
	assert.Equal(t, message.User, msgs[2].Role)
	assert.Equal(t, "next", msgs[2].Content().String(),
		"the queued prompt must run as its own turn")
	assert.Equal(t, message.Assistant, msgs[3].Role)
	assert.Equal(t, message.FinishReasonEndTurn, msgs[3].FinishReason())
}
