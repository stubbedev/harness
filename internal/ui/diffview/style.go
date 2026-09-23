package diffview

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
)

// LineStyle defines the styles for a given line type in the diff view.
type LineStyle struct {
	LineNumber lipgloss.Style
	Symbol     lipgloss.Style
	Code       lipgloss.Style
}

// Style defines the overall style for the diff view, including styles for
// different line types such as divider, missing, equal, insert, and delete
// lines.
type Style struct {
	DividerLine LineStyle
	MissingLine LineStyle
	EqualLine   LineStyle
	InsertLine  LineStyle
	DeleteLine  LineStyle
	Filename    LineStyle
}

// palette holds the colors a diff [Style] is built from; the light and
// dark defaults differ only in these.
type palette struct {
	dividerFg, dividerBg         color.Color // Hunk and file-name gutter.
	dividerCodeFg, dividerCodeBg color.Color // Hunk header text.
	gutterFg, gutterBg           color.Color // Line numbers of unchanged lines.
	text, textBg                 color.Color // Code of unchanged lines.
	insertNumBg, insertBg        color.Color
	deleteNumBg, deleteBg        color.Color
}

func (p palette) style() Style {
	s := lipgloss.NewStyle
	return Style{
		DividerLine: LineStyle{
			LineNumber: s().Foreground(p.dividerFg).Background(p.dividerBg),
			Code:       s().Foreground(p.dividerCodeFg).Background(p.dividerCodeBg),
		},
		MissingLine: LineStyle{
			LineNumber: s().Background(p.gutterBg),
			Code:       s().Background(p.gutterBg),
		},
		EqualLine: LineStyle{
			LineNumber: s().Foreground(p.gutterFg).Background(p.gutterBg),
			Code:       s().Foreground(p.text).Background(p.textBg),
		},
		InsertLine: LineStyle{
			LineNumber: s().Foreground(charmtone.Turtle).Background(p.insertNumBg),
			Symbol:     s().Foreground(charmtone.Turtle).Background(p.insertBg),
			Code:       s().Foreground(p.text).Background(p.insertBg),
		},
		DeleteLine: LineStyle{
			LineNumber: s().Foreground(charmtone.Cherry).Background(p.deleteNumBg),
			Symbol:     s().Foreground(charmtone.Cherry).Background(p.deleteBg),
			Code:       s().Foreground(p.text).Background(p.deleteBg),
		},
		Filename: LineStyle{
			LineNumber: s().Foreground(p.dividerFg).Background(p.dividerBg),
			Code:       s().Foreground(p.dividerFg).Background(p.dividerBg),
		},
	}
}

// DefaultLightStyle provides a default light theme style for the diff view.
func DefaultLightStyle() Style {
	return palette{
		dividerFg: charmtone.Iron, dividerBg: charmtone.Thunder,
		dividerCodeFg: charmtone.Oyster, dividerCodeBg: charmtone.Anchovy,
		gutterFg: charmtone.Char, gutterBg: charmtone.Sash,
		text: charmtone.Pepper, textBg: charmtone.Salt,
		insertNumBg: lipgloss.Color("#c8e6c9"), insertBg: lipgloss.Color("#e8f5e9"),
		deleteNumBg: lipgloss.Color("#ffcdd2"), deleteBg: lipgloss.Color("#ffebee"),
	}.style()
}

// DefaultDarkStyle provides a default dark theme style for the diff view.
func DefaultDarkStyle() Style {
	return palette{
		dividerFg: charmtone.Smoke, dividerBg: charmtone.Sapphire,
		dividerCodeFg: charmtone.Smoke, dividerCodeBg: charmtone.Ox,
		gutterFg: charmtone.Sash, gutterBg: charmtone.Char,
		text: charmtone.Salt, textBg: charmtone.Pepper,
		insertNumBg: lipgloss.Color("#293229"), insertBg: lipgloss.Color("#303a30"),
		deleteNumBg: lipgloss.Color("#332929"), deleteBg: lipgloss.Color("#3a3030"),
	}.style()
}
