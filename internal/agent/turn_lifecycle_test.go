package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

// callHookModel runs onCall before each request a wrapped model serves,
// numbered from 1, so a test can act at an exact point of a turn.
type callHookModel struct {
	fantasy.LanguageModel
	mu     sync.Mutex
	n      int
	onCall func(n int)
}

func (h *callHookModel) next() {
	h.mu.Lock()
	h.n++
	n := h.n
	h.mu.Unlock()
	if h.onCall != nil {
		h.onCall(n)
	}
}

func (h *callHookModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	h.next()
	return h.LanguageModel.Stream(ctx, call)
}

func (h *callHookModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	h.next()
	return h.LanguageModel.Generate(ctx, call)
}

// assertToolResultsPaired fails when a stored tool result has no stored
// call: history the model can no longer be sent.
func assertToolResultsPaired(t *testing.T, msgs []message.Message) {
	t.Helper()
	calls := map[string]bool{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls() {
			calls[tc.ID] = true
		}
	}
	for _, m := range msgs {
		for _, tr := range m.ToolResults() {
			assert.True(t, calls[tr.ToolCallID], "tool result %s has no call", tr.ToolCallID)
		}
	}
}

func TestFailedTurnHandsOffItsQueue(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "s")
	require.NoError(t, err)
	scripted := newScriptedModel(
		scriptedTurn{err: errors.New("provider returned 500")},
		scriptedTurn{text: "Answered the queued prompt."},
	)
	var sa *sessionAgent
	large := &callHookModel{LanguageModel: scripted}
	large.onCall = func(n int) {
		if n == 1 {
			_, queueErr := sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "Queued prompt", RunID: "r2"})
			require.NoError(t, queueErr, "a prompt sent to a busy session queues")
		}
	}
	sa = testSessionAgent(env, large, textModel("t"), "system").(*sessionAgent)
	zero := 0
	sa.maxRetries = &zero

	var mu sync.Mutex
	var first []notify.RunComplete
	_, _ = sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID, Prompt: "First", RunID: "r1", MaxOutputTokens: 10000,
		OnComplete: func(c notify.RunComplete) {
			mu.Lock()
			defer mu.Unlock()
			first = append(first, c)
		},
	})

	require.Len(t, scripted.sentCalls(), 2, "the queued prompt ran after the failure")
	assert.Empty(t, sa.QueuedPromptsList(sess.ID))
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, first, 1, "the failed turn reports once")
	assert.Equal(t, "r1", first[0].RunID)
	assert.Contains(t, first[0].Error, "provider returned 500", "the report carries the turn's error")
}

func TestContinuationAfterToolCallGetsItsOwnMessage(t *testing.T) {
	t.Parallel()

	tool := fantasy.NewAgentTool("probe", "probe", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("probed"), nil
	})
	env := testEnv(t)
	large := newScriptedModel(
		scriptedTurn{text: "part one", finish: fantasy.FinishReasonLength},
		scriptedTurn{text: " part two", calls: []scriptedCall{{name: "probe", input: map[string]any{}}}},
		scriptedTurn{text: "after tool"},
	)
	msgs := runScript(t, testSessionAgent(env, large, textModel("t"), "system", tool), env, "hi")

	var assistants []message.Message
	for _, m := range msgs {
		if m.Role == message.Assistant {
			assistants = append(assistants, m)
		}
	}
	require.Len(t, assistants, 2, "the resumed answer, then the step that answers its tool call")
	assert.Equal(t, "part one part two", assistants[0].Content().Text)
	assert.Len(t, assistants[0].ToolCalls(), 1)
	assert.Equal(t, "after tool", assistants[1].Content().Text)
	assertToolResultsPaired(t, msgs)
}

func TestRepeatedContinuationExtendsTheRequest(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	large := newScriptedModel(
		scriptedTurn{text: "part one", finish: fantasy.FinishReasonLength},
		scriptedTurn{text: " part two", finish: fantasy.FinishReasonLength},
		scriptedTurn{text: " part three"},
	)
	runScript(t, testSessionAgent(env, large, textModel("t"), "system"), env, "hi")

	calls := large.sentCalls()
	require.Len(t, calls, 3)
	second, third := calls[1].Prompt, calls[2].Prompt
	require.Greater(t, len(third), len(second))
	for i := range second {
		assert.Equal(t, messageText(second[i]), messageText(third[i]), "message %d of the second request is kept as sent", i)
		assert.Equal(t, second[i].Role, third[i].Role)
	}
	var nudges int
	for _, m := range third {
		if m.Role == fantasy.MessageRoleUser && messageText(m) == continuePrompt {
			nudges++
		}
	}
	assert.Equal(t, 2, nudges, "both nudges are in the third request")
}

func TestOverflowAtLaterStepKeepsCompletedSteps(t *testing.T) {
	t.Parallel()

	unit := "alpha7 beta9 gamma3 "
	per := approxTokenCount(strings.Repeat(unit, 1000))
	history := strings.Repeat(unit, int(150500*1000/per))
	tool := fantasy.NewAgentTool("probe", "probe", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse(strings.Repeat("delta5 omega2 ", 3000)), nil
	})
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "s")
	require.NoError(t, err)
	createMessage(t, env, sess.ID, message.User, message.TextContent{Text: history})
	earlier, err := env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "ok"}}})
	require.NoError(t, err)
	earlier.AddFinish(message.FinishReasonEndTurn, "", "")
	require.NoError(t, env.messages.Update(t.Context(), earlier))

	large := newScriptedModel(
		scriptedTurn{calls: []scriptedCall{{name: "probe", input: map[string]any{}}}},
		scriptedTurn{text: "Finished."},
	)
	agent := testSessionAgent(env, large, textModel("summary"), "system", tool)
	_, err = agent.Run(t.Context(), SessionAgentCall{Prompt: "Fix the parser", SessionID: sess.ID, MaxOutputTokens: 10000})
	require.NoError(t, err)

	stored, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	assertToolResultsPaired(t, stored)
	for _, m := range stored {
		if m.Role == message.Assistant {
			assert.NotEqual(t, message.FinishReasonError, m.FinishReason(), "no completed step is stamped with the overflow")
		}
	}
}

func TestOverflowRecoveryKeepsTheSessionBusy(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "s")
	require.NoError(t, err)
	large := newScriptedModel(
		scriptedTurn{text: "earlier answer"},
		scriptedTurn{err: errors.New("context_length_exceeded")},
		scriptedTurn{text: "Done."},
	)
	var sa *sessionAgent
	var busyDuringSummary []bool
	small := &callHookModel{LanguageModel: textModel("summary")}
	small.onCall = func(int) {
		busyDuringSummary = append(busyDuringSummary, sa.IsSessionBusy(sess.ID))
	}
	sa = testSessionAgent(env, large, small, "system").(*sessionAgent)
	_, err = sa.Run(t.Context(), SessionAgentCall{Prompt: "earlier", SessionID: sess.ID, MaxOutputTokens: 10000})
	require.NoError(t, err)
	busyDuringSummary = nil

	_, err = sa.Run(t.Context(), SessionAgentCall{Prompt: "hi", SessionID: sess.ID, MaxOutputTokens: 10000})
	require.NoError(t, err)
	require.NotEmpty(t, busyDuringSummary, "the recovery summarized the session")
	for _, busy := range busyDuringSummary {
		assert.True(t, busy, "the session reads busy while recovery compacts it")
	}
	assert.False(t, sa.IsSessionBusy(sess.ID))
}

// cancelOnVerdict cancels a session from inside the goal judge's verdict
// call: after the model has answered, before the turn decides whether
// to carry on.
type cancelOnVerdict struct {
	*judgeModel
	cancel func()
}

func (m *cancelOnVerdict) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	m.cancel()
	return m.judgeModel.Generate(ctx, call)
}

func TestCancelDuringTheGoalJudgeStopsEveryContinuation(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	createSimpleGoProject(t, env.workingDir)
	sess, err := env.sessions.Create(t.Context(), "s")
	require.NoError(t, err)
	goal, err := session.NewGoal("The parser is fixed", time.Now())
	require.NoError(t, err)
	require.NoError(t, env.sessions.SetGoal(t.Context(), sess.ID, goal))

	scripted := newScriptedModel(
		scriptedTurn{text: "Fixed the parser. Next I will run the test suite."},
		scriptedTurn{text: "Tests pass."},
	)
	var sa *sessionAgent
	judge := &cancelOnVerdict{
		judgeModel: newJudgeModel(`{"verdict": "not_met", "reason": "Tests not run."}`),
		cancel:     func() { sa.Cancel(sess.ID) },
	}
	agent, err := coderAgent(nil, env, scripted, judge)
	require.NoError(t, err)
	sa = agent.(*sessionAgent)
	sa.inherentGoals = true

	_, _ = sa.Run(t.Context(), SessionAgentCall{Prompt: "Fix the parser", SessionID: sess.ID, MaxOutputTokens: 10000})

	assert.Len(t, scripted.sentCalls(), 1, "neither the goal nor the next-steps continuation runs after a cancel")
	assert.Empty(t, sa.QueuedPromptsList(sess.ID))
}

func TestLiveInboxRestorePutsMessagesBackFirst(t *testing.T) {
	t.Parallel()

	in := newLiveInbox()
	require.NoError(t, in.record("p", SubagentInboxMessage{Text: "a"}))
	require.NoError(t, in.record("p", SubagentInboxMessage{Text: "b"}))
	drained := in.drain("p")
	require.NoError(t, in.record("p", SubagentInboxMessage{Text: "c"}))

	in.restore("p", drained[1:])

	var texts []string
	for _, m := range in.drain("p") {
		texts = append(texts, m.Text)
	}
	assert.Equal(t, []string{"b", "c"}, texts, "an undelivered message is read before newer ones")
}
