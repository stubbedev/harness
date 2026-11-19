package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

// recoveryFixture stores the state a process left when it died, through
// the same services a live turn writes with.
type recoveryFixture struct {
	t        *testing.T
	sessions session.Service
	messages message.Service
}

func newRecoveryFixture(t *testing.T) recoveryFixture {
	t.Helper()
	env := testEnv(t)
	return recoveryFixture{t: t, sessions: env.sessions, messages: env.messages}
}

func (f recoveryFixture) session() string {
	f.t.Helper()
	sess, err := f.sessions.Create(f.t.Context(), "crashed")
	require.NoError(f.t, err)
	_, err = f.messages.Create(f.t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "do it"}},
	})
	require.NoError(f.t, err)
	return sess.ID
}

func (f recoveryFixture) assistant(sessionID string, parts ...message.ContentPart) message.Message {
	f.t.Helper()
	msg, err := f.messages.Create(f.t.Context(), sessionID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: parts,
	})
	require.NoError(f.t, err)
	return msg
}

func (f recoveryFixture) result(sessionID, callID, name string) {
	f.t.Helper()
	_, err := f.messages.Create(f.t.Context(), sessionID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: callID, Name: name, Content: "done"}},
	})
	require.NoError(f.t, err)
}

func (f recoveryFixture) recover() {
	f.t.Helper()
	require.NoError(f.t, RecoverInterruptedTurns(f.t.Context(), f.messages))
}

func (f recoveryFixture) get(id string) message.Message {
	f.t.Helper()
	msg, err := f.messages.Get(f.t.Context(), id)
	require.NoError(f.t, err)
	return msg
}

// results returns every stored tool result of the session by call ID.
func (f recoveryFixture) results(sessionID string) map[string][]message.ToolResult {
	f.t.Helper()
	msgs, err := f.messages.List(f.t.Context(), sessionID)
	require.NoError(f.t, err)
	byCall := map[string][]message.ToolResult{}
	for _, msg := range msgs {
		for _, result := range msg.ToolResults() {
			byCall[result.ToolCallID] = append(byCall[result.ToolCallID], result)
		}
	}
	return byCall
}

// A process that died while a round of tools ran leaves the calls that
// had not returned without a result. Each gets one saying whether it is
// safe to call again, a call that returned keeps its own, and the turn is
// closed as interrupted.
func TestRecoverInterruptedTurnsClosesARoundCutOffMidTool(t *testing.T) {
	t.Parallel()
	f := newRecoveryFixture(t)
	sessionID := f.session()
	assistant := f.assistant(sessionID,
		message.TextContent{Text: "Reading, then editing."},
		message.ToolCall{ID: "read", Name: tools.ViewToolName, Input: `{"file_path":"a.go"}`, Finished: true},
		message.ToolCall{ID: "write", Name: tools.WriteToolName, Input: `{"file_path":"a.go"}`, Finished: true},
		message.ToolCall{ID: "look", Name: tools.FetchToolName, Input: `{"url":"https://example.com"}`, Finished: true},
	)
	f.result(sessionID, "read", tools.ViewToolName)

	f.recover()

	closed := f.get(assistant.ID)
	require.Equal(t, message.FinishReasonInterrupted, closed.FinishReason())
	require.True(t, closed.IsErrorLike(), "an interrupted turn renders its banner")
	require.Equal(t, "Reading, then editing.", closed.Content().Text)

	results := f.results(sessionID)
	require.Len(t, results["read"], 1, "a call that returned keeps only its own result")
	require.Equal(t, "done", results["read"][0].Content)

	require.Len(t, results["write"], 1)
	require.True(t, results["write"][0].IsError)
	require.Contains(t, results["write"][0].Content, "check its effects")

	require.Len(t, results["look"], 1)
	require.True(t, results["look"][0].IsError)
	require.Contains(t, results["look"][0].Content, "safe to call again")
}

// A process that died after the last result of a round but before the
// step recorded its finish lost nothing but the finish: the round is
// closed as the tool round it was, without a banner.
func TestRecoverInterruptedTurnsFinishesACompleteToolRound(t *testing.T) {
	t.Parallel()
	f := newRecoveryFixture(t)
	sessionID := f.session()
	assistant := f.assistant(sessionID,
		message.ToolCall{ID: "write", Name: tools.WriteToolName, Input: `{}`, Finished: true},
	)
	f.result(sessionID, "write", tools.WriteToolName)

	f.recover()

	closed := f.get(assistant.ID)
	require.Equal(t, message.FinishReasonToolUse, closed.FinishReason())
	require.False(t, closed.IsErrorLike())
	require.Len(t, f.results(sessionID)["write"], 1)
}

// A process that died mid-stream leaves a call whose arguments are cut
// off. The call is stored with empty arguments, so the history stays
// valid JSON, and its result says it never ran; thinking is closed.
func TestRecoverInterruptedTurnsClosesACallCutOffMidStream(t *testing.T) {
	t.Parallel()
	f := newRecoveryFixture(t)
	sessionID := f.session()
	assistant := f.assistant(sessionID,
		message.ReasoningContent{Thinking: "I should edit"},
		message.TextContent{Text: "Editing now."},
		message.ToolCall{ID: "edit", Name: tools.EditToolName, Input: `{"file_pa`, Finished: false},
	)

	f.recover()

	closed := f.get(assistant.ID)
	require.Equal(t, message.FinishReasonInterrupted, closed.FinishReason())
	require.NotZero(t, closed.ReasoningContent().FinishedAt)
	calls := closed.ToolCalls()
	require.Len(t, calls, 1)
	require.True(t, calls[0].Finished)
	require.JSONEq(t, `{}`, calls[0].Input)

	results := f.results(sessionID)["edit"]
	require.Len(t, results, 1)
	require.True(t, results[0].IsError)
	require.Contains(t, results[0].Content, "was not run")
}

// A process that died before the model said anything leaves an empty
// assistant message; it is closed as interrupted so the transcript shows
// what happened instead of a spinner.
func TestRecoverInterruptedTurnsClosesAnEmptyTurn(t *testing.T) {
	t.Parallel()
	f := newRecoveryFixture(t)
	sessionID := f.session()
	assistant := f.assistant(sessionID)

	f.recover()

	closed := f.get(assistant.ID)
	require.Equal(t, message.FinishReasonInterrupted, closed.FinishReason())
}

// Recovery closes the unfinished turns of every session, leaves finished
// messages alone, and finds nothing to do the second time.
func TestRecoverInterruptedTurnsCoversEverySessionOnce(t *testing.T) {
	t.Parallel()
	f := newRecoveryFixture(t)
	first, second := f.session(), f.session()
	done := f.assistant(first, message.TextContent{Text: "answered"})
	done.AddFinish(message.FinishReasonEndTurn, "", "")
	require.NoError(t, f.messages.Update(t.Context(), done))
	cutA := f.assistant(first, message.TextContent{Text: "half"})
	cutB := f.assistant(second, message.TextContent{Text: "also half"})

	f.recover()

	for id, want := range map[string]message.FinishReason{
		done.ID: message.FinishReasonEndTurn,
		cutA.ID: message.FinishReasonInterrupted,
		cutB.ID: message.FinishReasonInterrupted,
	} {
		msg := f.get(id)
		require.Equal(t, want, msg.FinishReason())
	}

	left, err := f.messages.ListUnfinishedAssistants(t.Context())
	require.NoError(t, err)
	require.Empty(t, left)
	f.recover()
}
