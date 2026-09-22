package model

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/ui/dialog"
	"github.com/stubbedev/harness/internal/version"
)

// sessionDetailsID identifies the session details window on the dialog
// stack.
const sessionDetailsID = "session-details"

// sessionDetailsDialog renders the session details window as a
// [dialog.Dialog], so it flows through the same overlay stack, key
// routing, and placement as every other window: the dismiss key closes
// it, and ctrl+d toggles it from either side. The window is passive
// (no selection, no input); it only reads UI state while drawing.
type sessionDetailsDialog struct {
	ui *UI
}

var (
	_ dialog.Dialog = (*sessionDetailsDialog)(nil)
	_ help.KeyMap   = (*sessionDetailsDialog)(nil)
)

// ID implements [dialog.Dialog].
func (d *sessionDetailsDialog) ID() string { return sessionDetailsID }

// HandleMsg implements [dialog.Dialog].
func (d *sessionDetailsDialog) HandleMsg(msg tea.Msg) dialog.Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	if key.Matches(k, dialog.CloseKey()) || key.Matches(k, d.ui.keyMap.Chat.Details) {
		return dialog.ActionClose{}
	}
	return nil
}

// ShortHelp implements [help.KeyMap]: the status bar's hint row follows
// the front dialog, so the window declares the keys it actually routes.
func (d *sessionDetailsDialog) ShortHelp() []key.Binding {
	return []key.Binding{
		dialog.CloseKey(),
		d.ui.keyMap.Chat.Details,
	}
}

// FullHelp implements [help.KeyMap].
func (d *sessionDetailsDialog) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}

// Draw implements [dialog.Dialog].
func (d *sessionDetailsDialog) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	if d.ui.session == nil || area.Dx() <= 0 || area.Dy() <= 1 {
		return nil
	}
	dialog.DrawCenter(scr, area, d.ui.sessionDetailsView(area))
	return nil
}

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

// detailsOpen reports whether the session details window is up.
func (m *UI) detailsOpen() bool {
	return m.dialog != nil && m.dialog.ContainsDialog(sessionDetailsID)
}

// toggleDetails opens or closes the session details window on the
// dialog stack, keeping ctrl+d a toggle from both sides.
func (m *UI) toggleDetails() {
	if m.dialog.ContainsDialog(sessionDetailsID) {
		m.dialog.CloseDialog(sessionDetailsID)
	} else {
		m.dialog.OpenDialog(&sessionDetailsDialog{ui: m})
	}
	m.updateLayoutAndSize()
}
