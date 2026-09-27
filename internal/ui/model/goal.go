package model

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/util"
)

// goalSetMsg reports that a goal was stored on the session; the turn
// that starts working toward it is sent on receipt, so the goal is in
// place before that turn can end and be judged.
type goalSetMsg struct {
	condition string
}

// setGoal stores condition as the session's goal and then starts
// working toward it. A session is created first when there is none.
func (m *UI) setGoal(condition string) tea.Cmd {
	goal, err := session.NewGoal(condition, time.Now())
	if err != nil {
		return util.ReportError(err)
	}
	if err := m.com.Workspace.AgentReadyErr(); err != nil {
		return util.ReportError(err)
	}
	var cmds []tea.Cmd
	if !m.hasSession() {
		newSession, err := m.com.Workspace.CreateSession(context.Background(), "New Session")
		if err != nil {
			return util.ReportError(err)
		}
		m.session = &newSession
		cmds = append(cmds, m.loadSession(newSession.ID))
		m.setState(uiChat, m.focus)
	}
	sessionID := m.session.ID
	cmds = append(cmds, func() tea.Msg {
		if err := m.com.Workspace.AgentSetGoal(context.Background(), sessionID, goal.Condition); err != nil {
			return util.ReportError(err)()
		}
		return goalSetMsg{condition: goal.Condition}
	})
	return tea.Batch(cmds...)
}

// clearGoal clears the current session's goal.
func (m *UI) clearGoal() tea.Cmd {
	if !m.hasSession() || !m.session.Goal.Active() {
		return util.ReportWarn("No goal is set")
	}
	sessionID := m.session.ID
	return func() tea.Msg {
		if err := m.com.Workspace.AgentSetGoal(context.Background(), sessionID, ""); err != nil {
			return util.ReportError(err)()
		}
		return util.ReportInfo("Goal cleared")()
	}
}
