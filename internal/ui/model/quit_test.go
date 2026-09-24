package model

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// ctrlC builds the ctrl+c key press the way the terminal delivers it.
func ctrlC() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
}

// isQuitCmd reports whether cmd is tea.Quit itself, without executing it
// (executing an arbitrary command tree could block on its timers).
func isQuitCmd(cmd tea.Cmd) bool {
	return cmd != nil &&
		reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(tea.Quit).Pointer()
}

// TestQuitRequiresDoublePress pins the Claude-style quit: the first ctrl+c
// arms a short window (and must not quit or open a dialog), the second
// press within it quits, and the timer expiring disarms so a later press
// starts over.
func TestQuitRequiresDoublePress(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	// First press: armed, nothing quit yet.
	_, cmd := m.Update(ctrlC())
	require.True(t, m.quitArm.state, "first ctrl+c must arm the quit window")
	require.False(t, isQuitCmd(cmd), "first ctrl+c must not quit")

	// Second press within the window quits.
	_, cmd = m.Update(ctrlC())
	require.False(t, m.quitArm.state, "second ctrl+c must disarm after quitting")
	require.True(t, isQuitCmd(cmd), "second ctrl+c must quit")
}

// TestCtrlCClearsDraftBeforeArming pins the Claude-style guard: with a
// drafted prompt the first ctrl+c clears the input instead of arming the
// quit window; the arm-then-quit sequence only starts from an empty input.
func TestCtrlCClearsDraftBeforeArming(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.textarea.InsertString("draft")

	_, cmd := m.Update(ctrlC())
	require.Empty(t, m.textarea.Value(), "first ctrl+c must clear the draft")
	require.False(t, m.quitArm.state, "clearing must not arm the quit window")
	require.False(t, isQuitCmd(cmd))

	_, cmd = m.Update(ctrlC())
	require.True(t, m.quitArm.state, "with the input empty the first press arms")
	require.False(t, isQuitCmd(cmd))
	_, cmd = m.Update(ctrlC())
	require.True(t, isQuitCmd(cmd), "the press inside the window quits")
}

// TestQuitTimerExpiryDisarms: once the window expires, a press only arms
// again instead of quitting outright.
func TestQuitTimerExpiryDisarms(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	m.Update(ctrlC())
	m.Update(quitTimerExpiredMsg{gen: m.quitArm.gen})
	require.False(t, m.quitArm.state, "timer expiry must disarm the quit window")

	_, cmd := m.Update(ctrlC())
	require.True(t, m.quitArm.state, "a press after expiry must re-arm, not quit")
	require.False(t, isQuitCmd(cmd))
}

// TestQuitHintFollowsRebind verifies the armed help binding names the
// configured quit key, not a hardcoded ctrl+c.
func TestQuitHintFollowsRebind(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.keyMap.ApplyKeybinds(map[string][]string{"quit": {"ctrl+q"}})

	m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	require.True(t, m.quitArm.state)

	for _, b := range m.ShortHelp() {
		if b.Help().Desc == "press again to quit" {
			require.Equal(t, "ctrl+q", b.Help().Key)
			return
		}
	}
	t.Fatal("armed quit binding not present in short help")
}

// TestStaleQuitTimerKeepsNewerArm: a timer left over from an earlier arming
// must not disarm a press made after it was started.
func TestStaleQuitTimerKeepsNewerArm(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	m.Update(ctrlC())
	stale := m.quitArm.gen
	m.Update(quitTimerExpiredMsg{gen: stale})
	m.Update(ctrlC())
	require.True(t, m.quitArm.state)

	m.Update(quitTimerExpiredMsg{gen: stale})
	require.True(t, m.quitArm.state, "a stale timer must not disarm a newer press")
}
