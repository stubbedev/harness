package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// TestIdleDoubleEscapeOpensRewind pins the idle half of the escape
// contract: with no LLM action running, a double escape opens the
// rewind picker — the cancel-last-message path. A draft or history
// browsing owns the press instead, and while the agent is busy escape
// stays the turn cancel.
func TestIdleDoubleEscapeOpensRewind(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	// First press arms.
	consumed, cmd := m.handleRewindEscape()
	require.True(t, consumed, "an unowned idle escape is consumed to arm")
	require.NotNil(t, cmd, "arming starts the shared cancel timer")
	require.True(t, m.rewindEscArmed)

	// Second press opens the picker.
	consumed, _ = m.handleRewindEscape()
	require.True(t, consumed)
	require.False(t, m.rewindEscArmed, "firing disarms")
	require.True(t, m.dialog.ContainsDialog(dialog.RewindID), "the rewind picker opens")
}

// TestRewindEscapeDeclinesWhenOwned pins that a draft, open
// completions, or history browsing swallow the escape without arming,
// and that a busy agent routes escape to the turn cancel instead.
func TestRewindEscapeDeclinesWhenOwned(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true}
	m := newBusyUI(ws)
	warmCaches(m, true)

	// Busy: never arms.
	consumed, _ := m.handleRewindEscape()
	require.False(t, consumed, "busy escape belongs to the turn cancel")
	require.False(t, m.rewindEscArmed)

	// Idle again, but a draft owns the press.
	warmCaches(m, false)
	m.textarea.InsertString("draft")
	consumed, _ = m.handleRewindEscape()
	require.False(t, consumed, "a draft swallows the escape")
	require.False(t, m.rewindEscArmed)
	m.textarea.Reset()

	// History browsing owns it too.
	m.promptHistory.messages = []string{"older prompt"}
	m.promptHistory.index = 0
	consumed, _ = m.handleRewindEscape()
	require.False(t, consumed, "history browsing swallows the escape")
	require.False(t, m.rewindEscArmed)
	m.promptHistory.index = -1

	// Arm, then a busy edge disarms.
	consumed, _ = m.handleRewindEscape()
	require.True(t, consumed)
	require.True(t, m.rewindEscArmed)
	warmCaches(m, true)
	consumed, _ = m.handleRewindEscape()
	require.False(t, consumed, "a run starting mid-window stops the rewind arm")
	require.False(t, m.rewindEscArmed)
}

// TestCancelArmingSuppressesRewindArm pins the mutual exclusion: the
// busy-path cancel arming clears a pending rewind arm, and vice versa,
// so the two double-escape meanings never mix.
func TestCancelArmingSuppressesRewindArm(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true}
	m := newBusyUI(ws)
	warmCaches(m, true)

	m.rewindEscArmed = true
	m.cancelAgent()
	require.False(t, m.rewindEscArmed, "cancel arming clears the rewind arm")

	// And the rewind arm clears a stale cancel arm.
	warmCaches(m, false)
	m.isCanceling = true
	m.handleRewindEscape()
	require.False(t, m.isCanceling, "rewind arming clears the cancel arm")
	assert.True(t, m.rewindEscArmed)
}
