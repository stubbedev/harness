package logo

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestRenderMark(t *testing.T) {
	t.Parallel()

	mark := RenderMark(lipgloss.NewStyle(), Opts{
		TitleColorA: color.RGBA{R: 255, A: 255},
		TitleColorB: color.RGBA{B: 255, A: 255},
	})

	lines := strings.Split(mark, "\n")
	require.Len(t, lines, 6, "the letter mark is six rows tall")
	for _, line := range lines {
		require.Equal(t, lipgloss.Width(lines[0]), lipgloss.Width(line), "block letters must stay rectangular")
	}
	require.Contains(t, mark, "\x1b[", "the mark must be painted with the gradient")

	truncated := RenderMark(lipgloss.NewStyle(), Opts{
		TitleColorA: color.RGBA{R: 255, A: 255},
		TitleColorB: color.RGBA{B: 255, A: 255},
		Width:       4,
	})
	for line := range strings.SplitSeq(truncated, "\n") {
		require.LessOrEqual(t, lipgloss.Width(ansi.Strip(line)), 4, "every line must honor the width")
	}
}
