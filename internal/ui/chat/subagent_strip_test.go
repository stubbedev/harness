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
	items := ExtractMessageItems(sty, msg, nil, "/tmp")

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
