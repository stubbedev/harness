package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func maxLineWidth(s string) int {
	w := 0
	for ln := range strings.SplitSeq(s, "\n") {
		w = max(w, ansi.StringWidth(ln))
	}
	return w
}

// Tool bodies render at the full body width with no readability cap:
// a pending call, its settled render, and the same call nested in a
// group all share one width budget, so the header does not change
// width when the call settles.
func TestToolWidthUsesFullBodyWidth(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	input := `{"query":"` + strings.Repeat("x", 400) + `"}`

	pending := NewToolMessageItem(&sty, "m", message.ToolCall{ID: "a", Name: "custom_tool", Input: input}, nil, false)
	settled := NewToolMessageItem(&sty, "m", message.ToolCall{ID: "b", Name: "custom_tool", Input: input, Finished: true},
		&message.ToolResult{ToolCallID: "b", Content: strings.Repeat("y ", 300)}, false)

	for _, width := range []int{80, 200} {
		require.Equal(t, ToolBodyWidth(width, 0), maxLineWidth(ansi.Strip(settled.RawRender(width))), "settled at %d", width)
		require.LessOrEqual(t, maxLineWidth(ansi.Strip(pending.RawRender(width))), ToolBodyWidth(width, 0), "pending at %d", width)
		require.Equal(t, ToolBodyWidth(width, 1), maxLineWidth(settled.BodyRender(ToolBodyWidth(width, 1))), "nested at %d", width)
	}
}
