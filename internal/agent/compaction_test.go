package agent

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

// compactionAgent is a session agent over a window of the given size,
// answering with a scripted large model and summarizing with a small
// model that says summaryText.
func compactionAgent(t *testing.T, env fakeEnv, window int64, summaryText string) (*sessionAgent, *scriptedModel) {
	t.Helper()
	small := textModel(summaryText)
	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{
			Model:      newScriptedModel(scriptedTurn{text: "ok"}),
			CatalogCfg: catalog.Model{ID: "large", ContextWindow: window, DefaultMaxTokens: 1000},
			ModelCfg:   config.SelectedModel{Model: "large", Provider: "scripted"},
		},
		SmallModel:   Model{Model: small, CatalogCfg: catalog.Model{ID: "small", ContextWindow: 1_000_000, DefaultMaxTokens: 1000}},
		SystemPrompt: "system",
		Sessions:     env.sessions,
		Messages:     env.messages,
	}).(*sessionAgent)
	return sa, small
}

func createMessage(t *testing.T, env fakeEnv, sessionID string, role message.MessageRole, parts ...message.ContentPart) message.Message {
	t.Helper()
	m, err := env.messages.Create(t.Context(), sessionID, message.CreateMessageParams{Role: role, Parts: parts})
	require.NoError(t, err)
	return m
}

// seedToolTurn writes one user question, the assistant's tool call and a
// tool result of the given size, so a history can be padded with the
// kind of bulk aging is for. It returns the tool result message.
func seedToolTurn(t *testing.T, env fakeEnv, sessionID string, n int, resultChars int) message.Message {
	t.Helper()
	createMessage(t, env, sessionID, message.User, message.TextContent{Text: fmt.Sprintf("question %d", n)})
	callID := fmt.Sprintf("call-%d", n)
	createMessage(t, env, sessionID, message.Assistant, message.ToolCall{ID: callID, Name: "view", Input: `{"file_path":"x"}`, Finished: true})
	return createMessage(t, env, sessionID, message.Tool, message.ToolResult{
		ToolCallID: callID, Name: "view", Content: strings.Repeat("line of file content\n", resultChars/22),
	})
}

// Past half the window, tool results from earlier turns are sent as
// stubs; the rows in the database keep their full content, and nothing
// is summarized yet.
func TestCompactionAgesOldToolResults(t *testing.T) {
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "aging")
	require.NoError(t, err)

	// Usable window 8000 tokens; a 20000-character result is ~5000, over
	// the aging line at 4000 and under the fold line at 6000.
	sa, small := compactionAgent(t, env, 9000, "unused")
	old := seedToolTurn(t, env, sess.ID, 1, 20000)
	createMessage(t, env, sess.ID, message.User, message.TextContent{Text: "question 2"})

	changed, err := sa.maintainContext(t.Context(), sess.ID, nil, nil, "auto", "", false)
	require.NoError(t, err)
	require.True(t, changed)

	updated, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, old.ID, updated.CompactionAgedID, "the watermark sits on the last message before the current turn")
	assert.Empty(t, updated.CompactionSummary, "aging alone was enough; nothing was summarized")
	assert.Empty(t, updated.CompactionBoundaryID)
	assert.Empty(t, small.sentCalls(), "the small model was not asked for anything")

	// What the model is sent has the stub; what is stored does not.
	sent, _, err := sa.sessionHistory(t.Context(), updated)
	require.NoError(t, err)
	var stubbed bool
	for _, m := range sent {
		for _, part := range m.Parts {
			if tr, ok := part.(message.ToolResult); ok {
				assert.Contains(t, tr.Content, "elided to save context")
				assert.Less(t, len(tr.Content), 300)
				stubbed = true
			}
		}
	}
	require.True(t, stubbed, "the old tool result must be sent as a stub")
	stored, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	for _, m := range stored {
		assert.False(t, m.IsSummaryMessage)
		for _, part := range m.Parts {
			if tr, ok := part.(message.ToolResult); ok {
				assert.Greater(t, len(tr.Content), 10000, "the stored row keeps the full result")
			}
		}
	}

	// A second pass with nothing new is a no-op: the watermark does not
	// creep, so the request prefix stays cacheable.
	changed, err = sa.maintainContext(t.Context(), sess.ID, nil, nil, "auto", "", false)
	require.NoError(t, err)
	assert.False(t, changed)
}

// Past three quarters of the window, the oldest history is folded into
// the hidden summary and the request restarts from a user turn. The
// transcript gains no message; the summary rides in as session memory.
func TestCompactionFoldsOldestHistoryIntoHiddenSummary(t *testing.T) {
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "folding")
	require.NoError(t, err)

	// Usable window 8000; five turns of ~2000 tokens of plain text each
	// blow well past the fold line, and aging has no tool results to
	// take out.
	sa, small := compactionAgent(t, env, 9000, "the gist of what happened")
	for i := 1; i <= 5; i++ {
		createMessage(t, env, sess.ID, message.User, message.TextContent{Text: fmt.Sprintf("question %d", i)})
		createMessage(t, env, sess.ID, message.Assistant, message.TextContent{Text: strings.Repeat(fmt.Sprintf("answer %d ", i), 900)})
	}

	changed, err := sa.maintainContext(t.Context(), sess.ID, nil, nil, "auto", "", false)
	require.NoError(t, err)
	require.True(t, changed)

	updated, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "the gist of what happened", updated.CompactionSummary)
	require.NotEmpty(t, updated.CompactionBoundaryID)
	assert.Empty(t, updated.CompactionAgedID, "a fold resets the aging watermark")
	require.Len(t, small.sentCalls(), 1, "one summary request, to the small model")

	stored, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, stored, 10, "no message was added or removed")
	for _, m := range stored {
		assert.False(t, m.IsSummaryMessage)
	}

	kept, summary, err := sa.sessionHistory(t.Context(), updated)
	require.NoError(t, err)
	require.NotEmpty(t, kept, "the most recent history stays verbatim")
	assert.Less(t, len(kept), 10)
	assert.Equal(t, message.User, kept[0].Role, "the verbatim tail opens on a user turn")
	assert.Contains(t, kept[len(kept)-1].Content().Text, "answer 5", "the latest turn is kept")

	history, _ := sa.preparePrompt(kept, true)
	history = withSummary(summary, history)
	require.NotEmpty(t, history)
	assert.Equal(t, fantasy.MessageRoleUser, history[0].Role)
	first := history[0].Content[0].(fantasy.TextPart).Text
	assert.Contains(t, first, "<session_memory>")
	assert.Contains(t, first, "the gist of what happened")
}

// A manual compaction folds everything typed so far, and the next fold
// carries the previous summary forward rather than starting over.
func TestManualCompactionFoldsEverythingAndRolls(t *testing.T) {
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "manual")
	require.NoError(t, err)
	sa, small := compactionAgent(t, env, 1_000_000, "first summary")
	createMessage(t, env, sess.ID, message.User, message.TextContent{Text: "first question"})
	last := createMessage(t, env, sess.ID, message.Assistant, message.TextContent{Text: "first answer"})

	require.NoError(t, sa.summarize(t.Context(), sess.ID, nil, nil, "manual", "keep the numbers"))

	updated, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "first summary", updated.CompactionSummary)
	assert.Equal(t, last.ID, updated.CompactionBoundaryID, "everything so far is behind the boundary")
	kept, _, err := sa.sessionHistory(t.Context(), updated)
	require.NoError(t, err)
	assert.Empty(t, kept)
	require.Len(t, small.sentCalls(), 1)
	assert.Contains(t, lastUserText(t, small.sentCalls()[0]), "keep the numbers")

	// More conversation, then another manual compaction: the previous
	// summary is part of what the small model is shown.
	createMessage(t, env, sess.ID, message.User, message.TextContent{Text: "second question"})
	createMessage(t, env, sess.ID, message.Assistant, message.TextContent{Text: "second answer"})
	sa.smallModel.Set(Model{Model: textModel("second summary")})
	require.NoError(t, sa.summarize(t.Context(), sess.ID, nil, nil, "manual", ""))

	updated, err = env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "second summary", updated.CompactionSummary)
	calls := sa.smallModel.Get().Model.(*scriptedModel).sentCalls()
	require.Len(t, calls, 1)
	var sawPrevious, sawNew bool
	for _, msg := range calls[0].Prompt {
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				sawPrevious = sawPrevious || strings.Contains(text.Text, "first summary")
				sawNew = sawNew || strings.Contains(text.Text, "second question")
			}
		}
	}
	assert.True(t, sawPrevious, "the rolling summary carries the previous one forward")
	assert.True(t, sawNew, "and folds the new messages")
}

// The fold boundary never lands on a tool result or an assistant turn:
// it is walked back to the user message that started the kept stretch.
func TestFoldCutStartsTailOnUserTurn(t *testing.T) {
	t.Parallel()
	a := &sessionAgent{}
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "q1"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: strings.Repeat("a", 4000)}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "q2"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "c", Name: "view", Input: "{}", Finished: true}}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "c", Content: strings.Repeat("b", 400)}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "done"}}},
	}
	// A budget that fits the last three messages but not q2's turn whole:
	// the cut still walks back to q2 so the tail starts on a user turn.
	assert.Equal(t, 2, a.foldCut(msgs, 200, true))
	// A budget with room for everything folds nothing.
	assert.Equal(t, 0, a.foldCut(msgs, 100_000, true))
	// A budget too small for even the last message keeps the last user
	// turn anyway.
	assert.Equal(t, 2, a.foldCut(msgs, 1, true))
}

// Stubbing leaves alone what a stub would not shorten and what is not
// text at all.
func TestStubAgedResultsLeavesShortAndMediaAlone(t *testing.T) {
	t.Parallel()
	msgs := []message.Message{
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "a", Name: "view", Content: strings.Repeat("x", 5000)},
			message.ToolResult{ToolCallID: "b", Name: "view", Content: "short"},
			message.ToolResult{ToolCallID: "c", Name: "view", Content: strings.Repeat("y", 5000), Data: "base64", MIMEType: "image/png"},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "d", Name: "shell", Content: strings.Repeat("z", 5000)},
		}},
	}
	out := stubAgedResults(msgs, 0)
	parts := out[0].Parts
	assert.Contains(t, parts[0].(message.ToolResult).Content, "view output")
	assert.Equal(t, "short", parts[1].(message.ToolResult).Content)
	assert.Equal(t, strings.Repeat("y", 5000), parts[2].(message.ToolResult).Content, "media results are not stubbed")
	assert.Equal(t, strings.Repeat("z", 5000), out[1].Parts[0].(message.ToolResult).Content, "past the watermark nothing changes")
	assert.Equal(t, strings.Repeat("x", 5000), msgs[0].Parts[0].(message.ToolResult).Content, "the input is not mutated")
}

// A session compacted before the boundary existed still resumes from its
// summary message, re-rooted as the user's turn.
func TestLegacySummaryMessageStillRootsTheHistory(t *testing.T) {
	t.Parallel()
	all := []message.Message{
		{ID: "old", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "old"}}},
		{ID: "sum", Role: message.Assistant, IsSummaryMessage: true, Parts: []message.ContentPart{message.TextContent{Text: "summary"}}},
		{ID: "new", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "new"}}},
	}
	tail, aged := compactedTail(session.Session{SummaryMessageID: "sum"}, all)
	require.Len(t, tail, 2)
	assert.Equal(t, "sum", tail[0].ID)
	assert.Equal(t, message.User, tail[0].Role)
	assert.Equal(t, -1, aged)
	assert.Equal(t, message.Assistant, all[1].Role, "the stored message is not re-rooted")
}

// Reasoning from finished turns is not sent again: only the current
// turn's thinking rides along, so a reasoning model's history does not
// carry every earlier deliberation forever.
func TestPreparePromptDropsEarlierTurnsReasoning(t *testing.T) {
	t.Parallel()
	a := &sessionAgent{}
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "first"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "old thinking", FinishedAt: 1},
			message.TextContent{Text: "first answer"},
		}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "second"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "current thinking", FinishedAt: 1},
			message.ToolCall{ID: "c", Name: "view", Input: "{}", Finished: true},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "c", Content: "x"}}},
	}
	history, _ := a.preparePrompt(msgs, true)
	var text strings.Builder
	for _, msg := range history {
		for _, part := range msg.Content {
			if r, ok := fantasy.AsMessagePart[fantasy.ReasoningPart](part); ok {
				text.WriteString(r.Text + "|")
			}
		}
	}
	assert.Equal(t, "current thinking|", text.String(), "only the current turn's reasoning is sent")
	assert.Equal(t, fantasy.MessageRoleAssistant, history[1].Role)
	assert.Len(t, history[1].Content, 1, "the earlier answer keeps its text and loses its thinking")
}
