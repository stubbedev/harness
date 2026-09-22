package dialog

import (
	"image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func paletteStyles() *styles.Styles {
	s := styles.ThemeForProvider("")
	return &s
}

// The bottom-anchored panel is full width with a top border only. The
// input sits above the closing rule: the panel frames the input between
// the content separator and the closing rule, and nothing follows it.
func TestRenderBottomAnchoredPutsInputLast(t *testing.T) {
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement(config.DialogPlacementBottom)

	st := paletteStyles()
	rc := NewRenderContext(st, 40)
	rc.Title = "Commands"
	rc.AddInput("> query")
	rc.AddPart("option one")
	rc.AddPart("option two")

	view := rc.Render()
	lines := strings.Split(ansi.Strip(view), "\n")

	require.True(t, strings.HasPrefix(lines[0], "──"), "panel opens with a top border:\n%s", view)
	require.False(t, strings.Contains(lines[0], "╭"), "no rounded floating corners:\n%s", view)
	last := lines[len(lines)-1]
	require.Empty(t, strings.Trim(strings.TrimSpace(last), "─"), "panel closes with a rule under the input:\n%s", view)
	input := lines[len(lines)-2]
	require.Contains(t, input, "> query", "input is the last content row, above the closing rule:\n%s", view)
	require.Contains(t, lines[len(lines)-3], "─", "the separator rule sits directly above the input, no margin row between:\n%s", view)
	// The input row is self-spaced in the hand-assembled panel: its own
	// margins carry the full content gutter, so the prompt aligns with
	// the padded content rows, and the caret (DialogCursor) reads the
	// same gutter from the input style alone.
	firstContent := strings.IndexFunc(lines[1], func(r rune) bool { return r != ' ' })
	firstInput := strings.IndexFunc(input, func(r rune) bool { return r != ' ' })
	require.Equal(t, firstContent, firstInput, "the input prompt aligns with the content gutter:\n%s", view)
	// A textinput's cursor starts at X = its prompt width ("❯ " is 2);
	// DialogCursor adds the gutter and nothing else, so the caret lands
	// exactly where typed text renders.
	cur := DialogCursor(st, view, &tea.Cursor{X: 2})
	require.Equal(t, firstInput+2, cur.X, "the caret sits where typed text renders, one prompt width in from the gutter")
	for _, l := range lines[1:] {
		require.False(t, strings.HasPrefix(l, "│"), "no side borders:\n%s", view)
		require.False(t, strings.Contains(l, "╰"), "no bottom corner:\n%s", view)
	}
}

// The noice-style floating mode keeps the classic framing: rounded box,
// input under the title, options below.
func TestRenderTopFloatingPutsInputUnderTitle(t *testing.T) {
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement(config.DialogPlacementTop)

	st := paletteStyles()
	rc := NewRenderContext(st, 40)
	rc.Title = "Commands"
	rc.AddInput("> query")
	rc.AddPart("option one")

	view := rc.Render()
	lines := strings.Split(ansi.Strip(view), "\n")

	require.Contains(t, lines[0], "╭", "floating box keeps its rounded border:\n%s", view)
	require.Contains(t, lines[len(lines)-1], "╰", "floating box keeps its bottom border:\n%s", view)
	inputIdx, optionIdx := -1, -1
	for i, l := range lines {
		if inputIdx < 0 && strings.Contains(l, "> query") {
			inputIdx = i
		}
		if optionIdx < 0 && strings.Contains(l, "option one") {
			optionIdx = i
		}
	}
	require.GreaterOrEqual(t, inputIdx, 1, "input renders under the title:\n%s", view)
	require.Less(t, inputIdx, optionIdx, "input precedes the options:\n%s", view)
}

func TestDialogWidthIsFullInBottomMode(t *testing.T) {
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement(config.DialogPlacementBottom)

	st := paletteStyles()
	area := uv.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(100, 40)}
	require.Equal(t, 100, DialogWidth(st, area))
	require.Equal(t, 98, DialogInnerWidth(st, 100), "only the panel's content gutter reduces the inner width")
}
