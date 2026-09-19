package model

import (
	"strings"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/ui/dialog"
	"github.com/stubbedev/harness/internal/version"
)

func (m *UI) sessionDetailsView(area uv.Rectangle) string {
	styles := m.com.Styles
	width := dialog.DialogWidth(styles, area)
	innerWidth := dialog.DialogInnerWidth(styles, width)
	height := min(sessionDetailsMaxHeight, max(0, area.Dy()-1))
	frame := dialog.ActiveFrame(styles)
	contentHeight := max(0, height-frame.GetVerticalFrameSize())
	if innerWidth == 0 || contentHeight == 0 {
		return ""
	}
	render := dialog.NewRenderContext(styles, width)
	render.Title = strings.Join(strings.Fields(m.session.Title), " ")
	headerHeight := lipgloss.Height(render.Render()) - frame.GetVerticalFrameSize()
	model := m.modelInfo(innerWidth)
	render.AddPart(model)
	remaining := max(0, contentHeight-headerHeight-lipgloss.Height(model)-1)
	sections := []func(int, int, bool) string{
		func(width, count int, section bool) string {
			return m.filesInfo(m.com.Workspace.WorkingDir(), width, count, section)
		},
		m.lspInfo, m.mcpInfo, m.skillsInfo,
	}
	if len(m.runningSubagents) > 0 {
		sections = append(sections, m.subagentsInfo)
	}
	columns := max(1, min(len(sections), (innerWidth+1)/19))
	sectionWidth := max(1, (innerWidth-columns+1)/columns)
	rows := (len(sections) + columns - 1) / columns
	rowHeight := remaining / rows
	if rowHeight >= 3 {
		for start := 0; start < len(sections); start += columns {
			var row []string
			for _, section := range sections[start:min(start+columns, len(sections))] {
				if len(row) > 0 {
					row = append(row, " ")
				}
				row = append(row, lipgloss.NewStyle().Width(sectionWidth).MaxWidth(sectionWidth).MaxHeight(rowHeight).Render(section(sectionWidth, max(0, rowHeight-3), false)))
			}
			render.AddPart(lipgloss.JoinHorizontal(lipgloss.Top, row...))
		}
	}
	render.AddPart(styles.CompactDetails.Version.Width(innerWidth).AlignHorizontal(lipgloss.Right).Render(ansi.Truncate(version.Version, innerWidth, "…")))
	return lipgloss.NewStyle().MaxWidth(width).MaxHeight(height).Render(render.Render())
}
