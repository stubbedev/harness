package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/anim"
)

// pulseGlyphSet reports whether s contains any spinner glyph, so tests
// can assert a tool line never carries one.
func pulseGlyphSet(s string) bool {
	for _, glyph := range anim.DefaultPulseGlyphs {
		if strings.Contains(s, glyph) {
			return true
		}
	}
	return false
}

func TestPendingToolRenderHasNoSpinner(t *testing.T) {
	t.Parallel()

	tool := bashTool("t1", "go build ./... && go test ./internal/ui/...", false)
	require.Equal(t, ToolStatusRunning, tool.EffectiveStatus())

	plain := ansi.Strip(tool.Render(80))
	require.False(t, pulseGlyphSet(plain), "pending tool render must not carry a spinner: %s", plain)
	require.Contains(t, plain, "Shell go build ./...")
	require.Contains(t, plain, "Waiting for tool response")
}

func TestPendingToolGroupExpandsToWaitingLine(t *testing.T) {
	t.Parallel()

	tool := bashTool("t1", "go build ./...", false)

	g := NewToolGroupMessageItem(groupStyles(), tool)
	collapsed := ansi.Strip(g.Render(80))
	require.Equal(t, 0, strings.Count(collapsed, "\n"))
	require.False(t, pulseGlyphSet(collapsed))

	g.DigIn()
	expanded := ansi.Strip(g.Render(80))
	require.False(t, pulseGlyphSet(expanded), "expanded pending tool must not carry a spinner: %s", expanded)
	require.Contains(t, expanded, "Waiting for tool response")
}

func TestPendingToolCompactStaysOneLine(t *testing.T) {
	t.Parallel()

	tool := bashTool("t1", "go build ./...", false)
	if c, ok := tool.(Compactable); ok {
		c.SetCompact(true)
	}

	plain := ansi.Strip(tool.Render(80))
	require.Equal(t, 0, strings.Count(plain, "\n"))
	require.NotContains(t, plain, "Waiting for tool response")
}

func TestPendingToolStreamsPartialDetail(t *testing.T) {
	t.Parallel()

	input := `{"command":"go build ./int`
	tool := NewToolMessageItem(groupStyles(), "msg", message.ToolCall{
		ID: "t1", Name: "shell", Input: input,
	}, nil, false)

	plain := ansi.Strip(tool.Render(80))
	require.False(t, pulseGlyphSet(plain))
	require.Contains(t, plain, "Shell")
	require.Contains(t, plain, "go build ./int")
	require.Contains(t, plain, "Waiting for tool response")
}
