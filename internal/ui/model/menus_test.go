package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/session"
)

// TestEscapeClosesAnyMenu pins the universal menu-close contract: every
// window flows through the dialog stack, so the dismiss key closes it
// wherever focus sits, with no per-surface special cases.
func TestEscapeClosesAnyMenu(t *testing.T) {
	newUI := func(t *testing.T) *UI {
		ui := newFrameTestUI(t)
		ui.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
		ui.state = uiChat
		ui.session = &session.Session{ID: "session", Title: "Details"}
		return ui
	}

	t.Run("details open and close on the dialog stack", func(t *testing.T) {
		ui := newUI(t)
		ui.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		require.True(t, ui.detailsOpen())

		ui.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		require.False(t, ui.detailsOpen())
	})

	t.Run("ctrl+d toggles from inside the dialog", func(t *testing.T) {
		ui := newUI(t)
		ui.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		require.True(t, ui.detailsOpen())

		ui.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		require.False(t, ui.detailsOpen())
	})

	t.Run("keys are routed to the dialog while open", func(t *testing.T) {
		ui := newUI(t)
		ui.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})

		ui.Update(tea.KeyPressMsg{Code: 'x', Mod: 0})
		require.True(t, ui.detailsOpen())
		require.Empty(t, ui.textarea.Value())
	})
}
