package model

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/ui/keys"
)

const (
	cancelTimerDuration = 2 * time.Second
	quitTimerDuration   = 1 * time.Second
)

// escArm is what the first escape press of a double-escape armed. The two
// meanings share one window and are mutually exclusive by construction:
// while the agent is busy escape cancels the turn, while idle it opens the
// rewind picker (the cancel-last-message path).
type escArm uint8

const (
	escNone escArm = iota
	escCancel
	escRewind
)

// timedArm is the state armed by the first press of a double-press key,
// disarmed when its window expires. Every change bumps gen, and the expiry
// message carries the gen it was started under, so a stale timer from an
// earlier press never disarms a newer one.
type timedArm[S comparable] struct {
	state S
	gen   uint64
}

// set moves to state s and returns the generation a timer for this arming
// must carry.
func (a *timedArm[S]) set(s S) uint64 {
	a.state = s
	a.gen++
	return a.gen
}

// clear disarms.
func (a *timedArm[S]) clear() {
	var zero S
	a.set(zero)
}

// expire disarms if gen is still the current arming.
func (a *timedArm[S]) expire(gen uint64) {
	if gen == a.gen {
		var zero S
		a.state = zero
	}
}

type (
	// cancelTimerExpiredMsg is sent when the escape window expires.
	cancelTimerExpiredMsg struct{ gen uint64 }
	// quitTimerExpiredMsg is sent when the quit window expires.
	quitTimerExpiredMsg struct{ gen uint64 }
)

// armEsc arms the escape window with s and starts its timer.
func (m *UI) armEsc(s escArm) tea.Cmd {
	gen := m.esc.set(s)
	return tea.Tick(cancelTimerDuration, func(time.Time) tea.Msg {
		return cancelTimerExpiredMsg{gen: gen}
	})
}

// armQuit arms the quit window and starts its timer.
func (m *UI) armQuit() tea.Cmd {
	gen := m.quitArm.set(true)
	return tea.Tick(quitTimerDuration, func(time.Time) tea.Msg {
		return quitTimerExpiredMsg{gen: gen}
	})
}

// escHintBinding returns the escape hint for the chat, if any: the cancel
// binding while the agent is busy (relabelled once armed), or the rewind
// prompt while idle with the first escape pressed. Esc acts only from the
// editor, so it is hinted only there. The cancel check runs before
// attachment delete mode consumes esc, so its hint stays accurate while
// that mode is armed; armed delete mode does consume esc before the
// rewind path, so the rewind hint yields to it.
func (m *UI) escHintBinding() (key.Binding, bool) {
	if m.focus != uiFocusEditor {
		return key.Binding{}, false
	}
	b := m.keyMap.Chat.Cancel
	switch {
	case m.isAgentBusy():
		if m.esc.state == escCancel {
			b.SetHelp(keys.HelpKeys(b), "press again to cancel")
		}
		return b, true
	case m.esc.state == escRewind && !m.attachments.Deleting():
		b.SetHelp(keys.HelpKeys(b), "press again to rewind")
		return b, true
	}
	return key.Binding{}, false
}

// quitHintBinding returns the quit binding, relabelled while armed.
func (m *UI) quitHintBinding() key.Binding {
	quit := m.keyMap.Quit
	if m.quitArm.state {
		quit.SetHelp(keys.HelpKeys(quit), "press again to quit")
	}
	return quit
}
