package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// strippedLines returns the item's render as trimmed, ANSI-stripped
// lines so assertions talk about visual structure, not styling.
func strippedLines(t *testing.T, render string) []string {
	t.Helper()
	lines := strings.Split(render, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(ansi.Strip(l))
	}
	return lines
}

// TestPromptInvocationRendersCompactRow pins the transcript shape: a
// named prompt invocation renders as a single tool-call style row, not
// the expanded prompt text echoed as a user message.
func TestPromptInvocationRendersCompactRow(t *testing.T) {
	t.Parallel()

	wrapped := message.FormatPromptInvocation("user:review",
		"Review the staged diff carefully.\n\nLook for bugs.")
	item := newTestUserItem(t, wrapped)

	lines := strippedLines(t, item.RawRender(100))
	require.Equal(t, []string{"Ran Prompt → user:review"}, lines,
		"the invocation collapses to one row without the body")
}

// TestPromptInvocationToggleExpanded pins expansion: space toggles the
// row open to the full prompt body and back, and the expand key never
// claims a plain user message.
func TestPromptInvocationToggleExpanded(t *testing.T) {
	t.Parallel()

	wrapped := message.FormatPromptInvocation("user:review",
		"Review the staged diff carefully.")
	item := newTestUserItem(t, wrapped)

	require.True(t, item.ToggleExpanded(), "first toggle expands")
	joined := strings.Join(strippedLines(t, item.RawRender(100)), "\n")
	require.Contains(t, joined, "Ran Prompt → user:review")
	require.Contains(t, joined, "Review the staged diff carefully.",
		"expansion shows the prompt body")

	require.False(t, item.ToggleExpanded(), "second toggle collapses")
	require.Equal(t, []string{"Ran Prompt → user:review"},
		strippedLines(t, item.RawRender(100)))
}

// TestPlainUserMessageNotExpandable pins that a typed user message
// reports ToggleExpanded as a no-op, like an assistant message without
// thinking.
func TestPlainUserMessageNotExpandable(t *testing.T) {
	t.Parallel()

	item := newTestUserItem(t, "just typed text")
	require.False(t, item.ToggleExpanded())
	require.NotContains(t, strings.Join(strippedLines(t, item.RawRender(80)), "\n"),
		"Ran Prompt")
}

// TestQueuedPromptInvocationRendersCompactRow pins the placeholder: a
// prompt queued as a named invocation shows the compact row while it
// waits, the same shape it takes once the real message lands, and a
// plain queued prompt keeps rendering as markdown.
func TestQueuedPromptInvocationRendersCompactRow(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	queued := NewQueuedMessageItem(&sty, "queued-1", []string{
		message.FormatPromptInvocation("user:review", "Review everything."),
		"plain follow-up",
	})

	lines := strippedLines(t, queued.RawRender(100))
	require.Contains(t, lines[0], styles.QueuedIcon, "the entry is marked queued")
	require.Contains(t, lines[0], "Ran Prompt → user:review")
	require.NotContains(t, strings.Join(lines, "\n"), "Review everything.",
		"the queued body must not be echoed")
	require.Contains(t, strings.Join(lines, "\n"), "plain follow-up")
}
