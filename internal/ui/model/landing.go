package model

import (
	"image"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/ultraviolet/layout"
	"github.com/stubbedev/harness/internal/ui/logo"
	"github.com/stubbedev/harness/internal/workspace"
)

// selectedLargeModel returns the currently selected large language model as
// memoized by the off-thread busy/agent probe (see workspace_cache.go), or
// nil when the agent isn't ready. It must never probe the workspace: it is
// called on every frame and AgentIsReady/AgentModel are synchronous HTTP
// round-trips in client/server mode.
func (m *UI) selectedLargeModel() *workspace.AgentModel {
	if m.agentReady {
		model := m.agentModel
		return &model
	}
	return nil
}

// landingView renders the landing page: the model information at the
// top and the gradient block-letter mark centered in the space between
// it and the editor. The working directory and git state are not here:
// the compact status line above the editor is their single home, in
// every state.
//
// The mark's placement is cached against the terminal size: typing
// grows the editor and shrinks the main area, and the mark must not
// re-center on every keystroke - only a resize recomputes it.
func (m *UI) landingView() string {
	t := m.com.Styles
	width := m.layout.main.Dx()

	infoSection := m.modelInfo(width)

	if size := image.Pt(m.width, m.height); m.landingMarkSize != size {
		m.landingMarkSize = size
		var remainingHeightArea image.Rectangle
		layout.Vertical(
			layout.Len(lipgloss.Height(infoSection)+1),
			layout.Fill(1),
		).Split(m.layout.main).Assign(new(image.Rectangle), &remainingHeightArea)

		mark := logo.RenderMark(t.Logo.GradCanvas, logo.Opts{
			TitleColorA: t.Logo.TitleColorA,
			TitleColorB: t.Logo.TitleColorB,
			Width:       width,
		})
		m.landingMarkView = lipgloss.Place(width, max(0, remainingHeightArea.Dy()), lipgloss.Center, lipgloss.Center, mark)
	}
	content := m.landingMarkView

	return lipgloss.NewStyle().
		Width(width).
		Height(m.layout.main.Dy() - 1).
		PaddingTop(1).
		Render(
			lipgloss.JoinVertical(lipgloss.Left, infoSection, content),
		)
}
