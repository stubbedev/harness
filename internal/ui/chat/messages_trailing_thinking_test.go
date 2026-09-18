package chat

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

// assistantMsg builds an assistant message from its parts.
func assistantMsg(parts ...message.ContentPart) *message.Message {
	return &message.Message{ID: "a1", Role: message.Assistant, Parts: parts}
}

func thinkingPart() message.ContentPart {
	return message.ReasoningContent{Thinking: "let me think", StartedAt: 1, FinishedAt: 2}
}

// A transcript must never end on a thinking entry: a turn cut off
// mid-generation persists a thinking-only assistant message, and
// rebuilding the transcript on resume has to drop it.
func TestTrimTrailingThinking(t *testing.T) {
	t.Parallel()

	user := &message.Message{
		ID: "u1", Role: message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hi"}},
	}
	thinkingOnly := assistantMsg(thinkingPart())
	withText := assistantMsg(thinkingPart(), message.TextContent{Text: "answer"})
	withToolCall := assistantMsg(thinkingPart(), message.ToolCall{ID: "t1", Name: "shell", Input: "{}"})
	canceled := assistantMsg(message.Finish{Reason: message.FinishReasonCanceled})
	withError := assistantMsg(message.Finish{Reason: message.FinishReasonError})

	t.Run("trailing thinking-only assistant is dropped", func(t *testing.T) {
		t.Parallel()
		got := TrimTrailingThinking([]*message.Message{user, thinkingOnly})
		require.Equal(t, []*message.Message{user}, got)
	})

	t.Run("assistant ending in text stays", func(t *testing.T) {
		t.Parallel()
		got := TrimTrailingThinking([]*message.Message{user, withText})
		require.Equal(t, []*message.Message{user, withText}, got)
	})

	t.Run("assistant with tool calls stays", func(t *testing.T) {
		t.Parallel()
		got := TrimTrailingThinking([]*message.Message{user, withToolCall})
		require.Equal(t, []*message.Message{user, withToolCall}, got)
	})

	t.Run("canceled assistant stays", func(t *testing.T) {
		t.Parallel()
		got := TrimTrailingThinking([]*message.Message{user, canceled})
		require.Equal(t, []*message.Message{user, canceled}, got)
	})

	t.Run("error assistant stays", func(t *testing.T) {
		t.Parallel()
		got := TrimTrailingThinking([]*message.Message{user, withError})
		require.Equal(t, []*message.Message{user, withError}, got)
	})

	t.Run("thinking-only in the middle stays", func(t *testing.T) {
		t.Parallel()
		got := TrimTrailingThinking([]*message.Message{user, thinkingOnly, withText})
		require.Equal(t, []*message.Message{user, thinkingOnly, withText}, got)
	})

	t.Run("all-thinking transcript empties", func(t *testing.T) {
		t.Parallel()
		got := TrimTrailingThinking([]*message.Message{assistantMsg(thinkingPart()), thinkingOnly})
		require.Empty(t, got)
	})

	t.Run("empty input stays empty", func(t *testing.T) {
		t.Parallel()
		require.Empty(t, TrimTrailingThinking(nil))
	})
}
