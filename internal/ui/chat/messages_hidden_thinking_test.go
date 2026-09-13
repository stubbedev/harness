package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// thinkingToolCallMsg is the mid-run shape of an interleaved-thinking
// turn (e.g. Claude thinking before a tool call) between the tool call
// landing on the message and the step finishing: a completed reasoning
// block, the tool call, and no Finish part yet. The tool is executing
// in this window.
func thinkingToolCallMsg() *message.Message {
	return &message.Message{
		ID:   "think-tool-1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{
				Thinking:   "Let me look at the issue first.",
				StartedAt:  1_000,
				FinishedAt: 2_000,
			},
			message.ToolCall{
				ID:       "toolu_1",
				Name:     "shell",
				Input:    `{"command":"gh issue view 18"}`,
				Finished: true,
			},
		},
	}
}

// TestHiddenThinkingToolCallNotRendered pins the fix for the empty
// transcript message that sat above a running tool: with thinking
// suppressed, a thinking-plus-tool-call turn has nothing to render, so
// the assistant text item must be dropped as soon as the tool call
// arrives. Before the fix the IsThinking() term kept the item alive
// until the step finish part landed (i.e. until the tool returned),
// rendering as a lone focused border.
func TestHiddenThinkingToolCallNotRendered(t *testing.T) {
	prev := HideThinking
	HideThinking = true
	t.Cleanup(func() { HideThinking = prev })

	msg := thinkingToolCallMsg()
	require.False(t, ShouldRenderAssistantMessage(msg),
		"a tool-only turn with hidden thinking must not keep the text item alive")

	// The item that the pre-fix update path left in the list renders
	// completely empty (just the border prefix), which is why it read
	// as a blank message above the running tool.
	sty := styles.CharmtonePantera()
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	out := item.Render(80)
	require.Empty(t, strings.TrimSpace(ansi.Strip(out)),
		"hidden thinking and no content must render nothing visible")
}

// TestVisibleThinkingToolCallStillRendered asserts the counterpart:
// when thinking is shown, the same turn keeps its item so the
// reasoning block that preceded the tool call stays in the transcript.
func TestVisibleThinkingToolCallStillRendered(t *testing.T) {
	prev := HideThinking
	HideThinking = false
	t.Cleanup(func() { HideThinking = prev })

	require.True(t, ShouldRenderAssistantMessage(thinkingToolCallMsg()),
		"visible thinking keeps the item so the reasoning block renders")
}

// TestHiddenThinkingBeforeToolCallKeepsSpinner covers the window the
// IsThinking term exists for: reasoning has started, nothing else has
// arrived yet. The placeholder (with its spinner) must survive even
// with thinking hidden, or the waiting phase would render nothing at
// all. Here the tool call has NOT landed, so !hasToolCalls already
// keeps the item; the test pins that the hidden-thinking gate must not
// regress this.
func TestHiddenThinkingBeforeToolCallKeepsSpinner(t *testing.T) {
	prev := HideThinking
	HideThinking = true
	t.Cleanup(func() { HideThinking = prev })

	msg := &message.Message{
		ID:   "think-only-1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "Hmm...", StartedAt: 1_000},
		},
	}
	require.True(t, ShouldRenderAssistantMessage(msg),
		"the streaming placeholder must render while the model is thinking")
}
