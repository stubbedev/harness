package model

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/ui/keys"
)

const quitTimerDuration = 1 * time.Second

// timedArm is the state armed by a press of a multi-press key, disarmed
// when its window expires. Every change bumps gen, and the expiry message
// carries the gen it was started under, so a stale timer from an earlier
// press never disarms a newer one.
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

// quitTimerExpiredMsg is sent when the quit window expires.
type quitTimerExpiredMsg struct{ gen uint64 }

// armQuit arms the quit window and starts its timer.
func (m *UI) armQuit() tea.Cmd {
	gen := m.quitArm.set(true)
	return tea.Tick(quitTimerDuration, func(time.Time) tea.Msg {
		return quitTimerExpiredMsg{gen: gen}
	})
}

// quitHintBinding returns the quit binding, relabelled while armed.
func (m *UI) quitHintBinding() key.Binding {
	quit := m.keyMap.Quit
	if m.quitArm.state {
		quit.SetHelp(keys.HelpKeys(quit), "press again to quit")
	}
	return quit
}
