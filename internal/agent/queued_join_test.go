package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

// TestRun_QueuedPromptsJoinIntoSingleUserMessage pins the join: prompts
// queued behind a busy session (no RunID, the TUI flow) run as one turn
// with a single user message joining every queued prompt, instead of one
// turn and one message each.
func TestRun_QueuedPromptsJoinIntoSingleUserMessage(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	large := &gatedStreamModel{
		text:    "done",
		gate:    make(chan struct{}),
		entered: make(chan struct{}),
	}
	sa := testSessionAgent(env, large, &finishStreamModel{text: "title"}, "system").(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		_, runErr := sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "main"})
		runDone <- runErr
	}()

	// Wait until the main turn is active (inside Stream), then queue two
	// follow-ups behind it the way prompts submitted mid-turn are.
	select {
	case <-large.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("main run never entered Stream")
	}
	require.True(t, sa.IsSessionBusy(sess.ID))
	sa.enqueueCall(SessionAgentCall{SessionID: sess.ID, Prompt: "first", acceptSeq: 1})
	sa.enqueueCall(SessionAgentCall{SessionID: sess.ID, Prompt: "second", acceptSeq: 2})
	require.Equal(t, 2, sa.QueuedPrompts(sess.ID))

	close(large.gate)
	select {
	case runErr := <-runDone:
		require.NoError(t, runErr, "the joined follow-up turn must complete normally")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the follow-up turn")
	}

	require.Equal(t, 0, sa.QueuedPrompts(sess.ID), "the queue must drain")

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 4, "main turn (user + assistant) then one joined follow-up turn")
	assert.Equal(t, message.User, msgs[0].Role)
	assert.Equal(t, "main", msgs[0].Content().String())
	assert.Equal(t, message.Assistant, msgs[1].Role)
	assert.Equal(t, message.User, msgs[2].Role)
	assert.Equal(t,
		"first"+message.QueuedPromptSeparator+"second",
		msgs[2].Content().String(),
		"both queued prompts must run as a single joined user message")
	assert.Equal(t, message.Assistant, msgs[3].Role)
}

// TestJoinQueuedCalls covers the merge itself: prompts join with the
// separator and attachments concatenate, while a single call passes
// through untouched.
func TestJoinQueuedCalls(t *testing.T) {
	t.Parallel()

	single := SessionAgentCall{SessionID: "s", Prompt: "solo"}
	assert.Equal(t, single, joinQueuedCalls([]SessionAgentCall{single}))

	joined := joinQueuedCalls([]SessionAgentCall{
		{SessionID: "s", Prompt: "one", Attachments: []message.Attachment{{FilePath: "a"}}},
		{SessionID: "s", Prompt: "two", Attachments: []message.Attachment{{FilePath: "b"}}},
		{SessionID: "s", Prompt: "three"},
	})
	assert.Equal(t, "one"+message.QueuedPromptSeparator+"two"+message.QueuedPromptSeparator+"three", joined.Prompt)
	assert.Equal(t, []message.Attachment{{FilePath: "a"}, {FilePath: "b"}}, joined.Attachments)
}
