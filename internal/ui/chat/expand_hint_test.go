package chat

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func TestThinkingHintsNameReboundExpandKey(t *testing.T) {
	t.Cleanup(func() { keys.Install(nil) })
	keys.Install(map[string][]string{"chat.expand": {"e"}})

	sty := styles.CharmtonePantera()

	collapsed := NewAssistantMessageItem(&sty, thinkingMessageWithLines("hint-collapsed", 50)).(*AssistantMessageItem)
	plain := ansi.Strip(collapsed.RawRender(97))
	require.Contains(t, plain, "[click or e to expand]")
	require.NotContains(t, plain, "space")

	tail := NewAssistantMessageItem(&sty, thinkingMessageWithLines("hint-tail", maxExpandedThinkingTailLines*2)).(*AssistantMessageItem)
	require.True(t, tail.ToggleExpanded())
	require.Equal(t, thinkingTailWindow, tail.thinkingViewMode)
	plain = ansi.Strip(tail.RawRender(97))
	require.Contains(t, plain, "[click or e for full view]")
	require.NotContains(t, plain, "space")
}
