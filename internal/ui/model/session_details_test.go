package model

import (
	"fmt"
	"image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

func TestSessionDetailsSharesPaletteFrameAndAnchor(t *testing.T) {
	t.Cleanup(func() { dialog.InstallPlacement(config.DialogPlacementBottom) })
	for _, placement := range []string{config.DialogPlacementBottom, config.DialogPlacementTop} {
		t.Run(placement, func(t *testing.T) {
			dialog.InstallPlacement(placement)
			ui := newTestUIWithConfig(t, &config.Config{Options: &config.Options{}})
			ui.session = &session.Session{Title: "Session details"}
			for _, size := range []image.Point{{120, 40}, {80, 24}, {40, 16}, {20, 8}, {5, 3}, {1, 1}, {0, 0}} {
				t.Run(fmt.Sprint(size), func(t *testing.T) {
					area := image.Rectangle{Min: image.Pt(3, 2), Max: image.Pt(3+size.X, 2+size.Y)}
					view := ui.sessionDetailsView(area)
					require.LessOrEqual(t, lipgloss.Width(view), size.X)
					if view != "" {
						require.LessOrEqual(t, lipgloss.Height(view), max(0, size.Y-1))
					}
					actual := uv.NewScreenBuffer(area.Max.X+2, area.Max.Y+2)
					ui.drawSessionDetails(actual, area)
					expected := uv.NewScreenBuffer(area.Max.X+2, area.Max.Y+2)
					if view != "" {
						dialog.DrawCenter(expected, area, view)
					}
					for y := range area.Max.Y + 2 {
						for x := range area.Max.X + 2 {
							require.Equal(t, expected.CellAt(x, y), actual.CellAt(x, y), "cell %d,%d", x, y)
						}
					}
					if size.X >= 40 && size.Y >= 16 {
						plain := ansi.Strip(view)
						require.Contains(t, plain, "Session details")
						if placement == config.DialogPlacementBottom {
							require.True(t, strings.HasPrefix(plain, "──"))
							require.NotContains(t, plain, "╭")
							anchor := dialog.AnchorRect(area, lipgloss.Width(view), lipgloss.Height(view))
							require.Equal(t, area.Max.Y-1, anchor.Max.Y)
						} else {
							require.Contains(t, plain, "╭")
						}
					}
				})
			}
		})
	}
}

func TestSessionDetailsTogglePreservesStatusAndChatLayout(t *testing.T) {
	dialog.InstallPlacement(config.DialogPlacementBottom)
	t.Cleanup(func() { dialog.InstallPlacement(config.DialogPlacementBottom) })
	ui := newFrameTestUI(t)
	ui.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
	ui.session = &session.Session{ID: "session", Title: "Details toggle marker"}
	before := ui.View()
	beforeLayout := ui.layout
	ui.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	require.True(t, ui.detailsOpen)
	opened := viewChecked(t, ui, "details opened")
	require.Equal(t, beforeLayout.main, ui.layout.main)
	require.Equal(t, beforeLayout.editor, ui.layout.editor)
	beforeRows := strings.Split(ansi.Strip(before.Content), "\n")
	openedRows := strings.Split(ansi.Strip(opened.Content), "\n")
	require.Equal(t, beforeRows[len(beforeRows)-1], openedRows[len(openedRows)-1])
	ui.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	require.False(t, ui.detailsOpen)
	closed := viewChecked(t, ui, "details closed")
	require.Equal(t, before.Content, closed.Content)
}

func TestSessionDetailsNilSessionDoesNotDraw(t *testing.T) {
	t.Parallel()
	ui := newTestUIWithConfig(t, &config.Config{Options: &config.Options{}})
	screen := uv.NewScreenBuffer(80, 24)
	require.NotPanics(t, func() { ui.drawSessionDetails(screen, screen.Bounds()) })
}
