package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/chat"
)

// TestHiddenThinkingToolRunDropsEmptyAssistant reproduces the reported
// transcript bug: with thinking suppressed (options.tui.show_thinking
// = false), a turn that thinks and then calls a tool left an empty
// assistant message (a lone focused border) above the running tool for
// the whole execution, because ShouldRenderAssistantMessage's
// IsThinking term kept the placeholder alive until the step's Finish
// part landed — which is only after the tool result arrives.
func TestHiddenThinkingToolRunDropsEmptyAssistant(t *testing.T) {
	prev := chat.HideThinking
	chat.HideThinking = true
	t.Cleanup(func() { chat.HideThinking = prev })

	u := liveFlowUI()

	userMsg := message.Message{ID: "u1", Role: message.User, Parts: []message.ContentPart{
		message.TextContent{Text: "I have another chat working concurrently. dont mind him"},
	}}
	_ = u.appendSessionMessage(userMsg)

	// Assistant message created empty (stream placeholder).
	assistant := message.Message{ID: "m1", Role: message.Assistant}
	_ = u.appendSessionMessage(assistant)
	require.NotNil(t, u.chat.MessageItem("m1"), "stream placeholder should exist")

	// Interleaved thinking streams, then the tool call lands. This is
	// the message state while the bash tool executes: reasoning block
	// done, tool call present, no Finish part yet.
	running := message.Message{ID: "m1", Role: message.Assistant, Parts: []message.ContentPart{
		message.ReasoningContent{
			Thinking:   "The previous output was empty, pipe through cat.",
			StartedAt:  1_000,
			FinishedAt: 2_000,
		},
		message.ToolCall{
			ID:       "toolu_1",
			Name:     "Bash",
			Input:    `{"command":"gh issue view 18 | cat"}`,
			Finished: true,
		},
	}}
	_ = u.updateSessionMessage(running)

	require.NotNil(t, u.chat.ToolItem("toolu_1"), "the running bash tool item must exist")
	_, isAssistant := u.chat.MessageItem("m1").(*chat.AssistantMessageItem)
	assert.False(t, isAssistant,
		"the empty assistant item must be dropped while the tool runs, not linger until the result arrives")
}
