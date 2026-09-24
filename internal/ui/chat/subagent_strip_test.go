package chat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

func TestExtractMessageItemsSkipsSubagentTools(t *testing.T) {
	t.Parallel()
	sty := groupStyles()

	msg := &message.Message{
		ID:   "m1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "a1", Name: "agent", Input: `{"prompt":"go dig"}`, Finished: true},
			message.ToolCall{ID: "b1", Name: "Bash", Input: `{"command":"ls"}`, Finished: true},
			message.ToolCall{ID: "r1", Name: "research", Input: `{"query":"go dig"}`, Finished: true},
		},
	}
	items := ExtractMessageItems(sty, msg, nil)

	require.Len(t, items, 1, "only the bash call renders in the transcript")
	tool, ok := items[0].(ToolMessageItem)
	require.True(t, ok)
	assert.Equal(t, "b1", tool.ID())
}

func TestIsSubagentTool(t *testing.T) {
	t.Parallel()

	assert.True(t, IsSubagentTool("agent"))
	assert.True(t, IsSubagentTool("research"))
	assert.False(t, IsSubagentTool("bash"))
	assert.False(t, IsSubagentTool("mcp_foo"))
}

func TestExtractMessageItemsSkipsInternalContextTools(t *testing.T) {
	t.Parallel()
	sty := groupStyles()

	msg := &message.Message{
		ID:   "m1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "s1", Name: "skill_search", Input: `{"query":"test"}`, Finished: true},
			message.ToolCall{ID: "t1", Name: "tool_search", Input: `{"load":["bash"]}`, Finished: true},
			message.ToolCall{ID: "g1", Name: "grep", Input: `{"pattern":"foo"}`, Finished: true},
		},
	}
	items := ExtractMessageItems(sty, msg, nil)

	require.Len(t, items, 1, "only the grep call renders in the transcript")
	tool, ok := items[0].(ToolMessageItem)
	require.True(t, ok)
	assert.Equal(t, "g1", tool.ID())
}

func TestIsInternalContextTool(t *testing.T) {
	t.Parallel()

	assert.True(t, IsInternalContextTool("skill_search"))
	assert.True(t, IsInternalContextTool("tool_search"))
	assert.True(t, IsInternalContextTool("send_message"))
	assert.False(t, IsInternalContextTool("agent"))
	assert.False(t, IsInternalContextTool("bash"))
}

// TestExtractMessageItemsSkipsSubagentNotes pins the report-back
// contract: a send_message delivered as a user message is LLM-to-LLM
// context that never becomes a transcript item. The persisted shape
// carries a trailing Finish bookkeeping part — persistence appends one
// to every non-assistant message — which must not turn the note into a
// rendered, empty user bubble.
func TestExtractMessageItemsSkipsSubagentNotes(t *testing.T) {
	t.Parallel()
	sty := groupStyles()

	noteOnly := &message.Message{
		ID:   "n1",
		Role: message.User,
		Parts: []message.ContentPart{
			message.SubagentNote{AgentName: "researcher", Handle: "bg-1", ChildSessionID: "c1", Text: "halfway there"},
			message.Finish{Reason: "stop"},
		},
	}
	require.Empty(t, ExtractMessageItems(sty, noteOnly, nil))

	real := &message.Message{
		ID:   "u1",
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "go dig"},
		},
	}
	require.Len(t, ExtractMessageItems(sty, real, nil), 1)
}
