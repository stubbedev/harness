// Package logo renders a Harness wordmark in a stylized way.
package logo

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// wordmark is the "Harness" wordmark in ANSI Shadow block letters.
const wordmark = `██╗  ██╗ █████╗ ██████╗ ███╗   ██╗███████╗███████╗███████╗
██║  ██║██╔══██╗██╔══██╗████╗  ██║██╔════╝██╔════╝██╔════╝
███████║███████║██████╔╝██╔██╗ ██║█████╗  ███████╗███████╗
██╔══██║██╔══██║██╔══██║██║╚██╗██║██╔══╝  ╚════██║╚════██║
██║  ██║██║  ██║██║  ██║██║ ╚████║███████╗███████╗███████║
╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═══╝╚══════╝╚══════╝╚══════╝`

// letterMark is the "H" of the wordmark in ANSI Shadow block letters.
const letterMark = `██╗  ██╗
██║  ██║
███████║
██╔══██║
██║  ██║
╚═╝  ╚═╝`

// Opts are the options for rendering the Harness logo.
type Opts struct {
	TitleColorA  color.Color // left gradient ramp point
	TitleColorB  color.Color // right gradient ramp point
	VersionColor color.Color // version text color
	Width        int         // width of the rendered logo, used for truncation
}

// Render renders the Harness logo: the block wordmark with the version
// right-aligned in the meta row above it.
func Render(base lipgloss.Style, version string, o Opts) string {
	firstLine, _, _ := strings.Cut(wordmark, "\n")
	markWidth := lipgloss.Width(firstLine)
	mark := gradientMark(base, wordmark, o.TitleColorA, o.TitleColorB)

	// Version, right-aligned in the meta row above the wordmark.
	version = ansi.Truncate(version, markWidth, "…") // truncate version if too long.
	gap := max(0, markWidth-lipgloss.Width(version))
	metaRow := strings.Repeat(" ", gap) + lipgloss.NewStyle().Foreground(o.VersionColor).Render(version)
	return truncateLines(strings.TrimSpace(metaRow+"\n"+mark), o.Width)
}

// RenderMark renders the block letter mark painted with the title
// gradient - the single source for every surface that shows the
// standalone "H".
func RenderMark(base lipgloss.Style, o Opts) string {
	return truncateLines(gradientMark(base, letterMark, o.TitleColorA, o.TitleColorB), o.Width)
}

// gradientMark paints each line of a block-letter mark with the title
// gradient so the color ramps left to right across the glyphs.
func gradientMark(base lipgloss.Style, mark string, colorA, colorB color.Color) string {
	b := new(strings.Builder)
	for line := range strings.SplitSeq(mark, "\n") {
		b.WriteString(styles.ApplyForegroundGrad(base, line, colorA, colorB))
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// truncateLines truncates every line of a multi-line string to width.
func truncateLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "")
	}
	return strings.Join(lines, "\n")
}
