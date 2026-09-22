package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/completions"
)

// TestEscapeClosesAnyMenu pins the universal menu-close contract: the
// dialog dismiss key closes whichever menu-like surface is on top,
// regardless of where focus sits. Dialogs themselves are covered by the
// dialog stack's own routing on the same binding.
func TestEscapeClosesAnyMenu(t *testing.T) {
	t.Run("details", func(t *testing.T) {
		ui := newFrameTestUI(t)
		ui.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
		ui.session = &session.Session{ID: "session", Title: "Details"}
		ui.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		require.True(t, ui.detailsOpen)

		ui.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		require.False(t, ui.detailsOpen)
	})

	t.Run("completions", func(t *testing.T) {
		ui := newFrameTestUI(t)
		ui.completions = completions.New(
			ui.com.Styles.Completions.Normal,
			ui.com.Styles.Completions.Focused,
			ui.com.Styles.Completions.Match,
		)
		ui.completionsOpen = true

		ui.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		require.False(t, ui.completionsOpen)
	})

	t.Run("expanded pills", func(t *testing.T) {
		ui := newFrameTestUI(t)
		ui.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
		ui.session = &session.Session{ID: "session", Todos: []session.Todo{
			{Content: "task", Status: session.TodoStatusInProgress},
		}}
		ui.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
		require.True(t, ui.pillsExpanded)

		ui.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		require.False(t, ui.pillsExpanded)
	})

	t.Run("escape with no menu leaves focus state alone", func(t *testing.T) {
		ui := newFrameTestUI(t)
		require.False(t, ui.closeTopMenu())
		require.Equal(t, uiFocusMain, ui.focus)
	})
}
