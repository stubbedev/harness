package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
)

// smallWindowModel is a model whose usable window is far below what the
// seeded history needs, standing in for the model a mid-session switch
// leaves you on.
func smallWindowModel(m *scriptedModel, window, maxTokens int64) Model {
	return Model{
		Model: m,
		CatalogCfg: catalog.Model{
			ID:               "small-window",
			ContextWindow:    window,
			DefaultMaxTokens: maxTokens,
		},
		ModelCfg: config.SelectedModel{Model: "small-window", Provider: "scripted"},
	}
}

// TestSwitchingToASmallerModelCompactsFirst is the model-switch case: a
// session whose history fit the model it was built on no longer fits the
// one now answering. The turn must compact and carry on rather than send
// a request the new model cannot take.
//
// Nothing here knows a switch happened. The check is on whether the
// history fits the model about to receive it, which also covers resuming
// an old session on a smaller model, and leaves a switch that still fits
// alone.
func TestSwitchingToASmallerModelCompactsFirst(t *testing.T) {
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Long conversation")
	require.NoError(t, err)

	// A history far larger than the window it is about to meet.
	big := strings.Repeat("some earlier conversation. ", 4000)
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: big}},
	})
	require.NoError(t, err)

	model := newScriptedModel(scriptedTurn{text: "summary of the conversation so far"})
	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel:   smallWindowModel(model, 4000, 1000),
		SmallModel:   Model{Model: textModel("title")},
		SystemPrompt: "test",
		Sessions:     env.sessions,
		Messages:     env.messages,
	})

	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "carry on"})
	require.NoError(t, err, "the turn must recover rather than surface the overflow")

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)

	// The turn that never happened leaves nothing behind: no second copy
	// of the prompt, no errored assistant shell for a failure that was
	// recovered from, and no summary message either - compaction lives on
	// the session, not in the transcript.
	var prompts, errored int
	for _, m := range msgs {
		assert.False(t, m.IsSummaryMessage, "compaction must not write into the transcript")
		if m.Role == message.User && strings.Contains(m.Content().Text, "carry on") {
			prompts++
		}
		if m.Role == message.Assistant && m.IsErrorLike() {
			errored++
		}
	}
	assert.Equal(t, 1, prompts, "the prompt is written once, after the compaction")
	assert.Zero(t, errored, "a recovered overflow is not reported as a failed turn")

	// The session now carries the summary and resumes from the boundary,
	// so the prompt that triggered all this was answered against the
	// compacted conversation. The summary is the small model's work.
	updated, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "title", updated.CompactionSummary, "the small model writes the summary")
	assert.NotEmpty(t, updated.CompactionBoundaryID)
	assert.Empty(t, updated.SummaryMessageID, "no summary message to resume from")

	kept, summary, err := (&sessionAgent{messages: env.messages}).sessionHistory(t.Context(), updated)
	require.NoError(t, err)
	assert.Equal(t, updated.CompactionSummary, summary)
	for _, m := range kept {
		assert.NotContains(t, m.Content().Text, "some earlier conversation",
			"the history the summary replaced is no longer sent")
	}
}

// A history that still fits the model must not be compacted: the trigger
// is the context not fitting, not the switch itself.
func TestFittingHistoryIsNotCompacted(t *testing.T) {
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Short conversation")
	require.NoError(t, err)

	model := newScriptedModel(scriptedTurn{text: "ack"})
	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel:   smallWindowModel(model, 1_000_000, 1000),
		SmallModel:   Model{Model: textModel("title")},
		SystemPrompt: "test",
		Sessions:     env.sessions,
		Messages:     env.messages,
	})

	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hello"})
	require.NoError(t, err)

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	for _, m := range msgs {
		assert.False(t, m.IsSummaryMessage, "a history that fits must be left alone")
	}
	updated, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Empty(t, updated.CompactionSummary)
	assert.Empty(t, updated.CompactionBoundaryID)
	assert.Empty(t, updated.CompactionAgedID)
}
