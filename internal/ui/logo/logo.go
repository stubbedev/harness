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
	fg := func(c color.Color, s string) string {
		return lipgloss.NewStyle().Foreground(c).Render(s)
	}

	firstLine, _, _ := strings.Cut(wordmark, "\n")
	markWidth := lipgloss.Width(firstLine)

	// Wordmark, each line painted with the title gradient.
	b := new(strings.Builder)
	for line := range strings.SplitSeq(wordmark, "\n") {
		b.WriteString(styles.ApplyForegroundGrad(base, line, o.TitleColorA, o.TitleColorB))
		b.WriteByte('\n')
	}
	mark := b.String()

	// Version, right-aligned in the meta row above the wordmark.
	version = ansi.Truncate(version, markWidth, "…") // truncate version if too long.
	gap := max(0, markWidth-lipgloss.Width(version))
	metaRow := strings.Repeat(" ", gap) + fg(o.VersionColor, version)

	logo := strings.TrimSpace(metaRow + "\n" + mark)

	if o.Width > 0 {
		// Truncate the logo to the specified width.
		lines := strings.Split(logo, "\n")
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, o.Width, "")
		}
		logo = strings.Join(lines, "\n")
	}
	return logo
}
