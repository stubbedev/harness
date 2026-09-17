package dialog

import (
	"image"
	"strings"
	"testing"

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

// The bottom-anchored panel is full width with a top border only, and the
// input is the last line: the options sit above it and nothing follows.
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
	require.Contains(t, last, "> query", "input is the bottommost line:\n%s", view)
	require.NotEqual(t, "", strings.TrimSpace(last), "no trailing blank line under the input:\n%s", view)
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
