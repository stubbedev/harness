package model

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

// goalSetMsg reports that a goal was stored on the session; the turn
// that starts working toward it is sent on receipt, so the goal is in
// place before that turn can end and be judged.
type goalSetMsg struct {
	sessionID string
	condition string
	created   *session.Session
}

// setGoal stores condition as the session's goal and then starts
// working toward it. A session is created first when there is none.
func (m *UI) setGoal(condition string) tea.Cmd {
	goal, err := session.NewGoal(condition, time.Now())
	if err != nil {
		return util.ReportError(err)
	}
	ws := m.com.Workspace
	sessionID := m.currentSessionID()
	return func() tea.Msg {
		return storeGoal(ws, sessionID, goal.Condition)
	}
}

func storeGoal(ws workspace.Workspace, sessionID, condition string) tea.Msg {
	if err := ws.AgentReadyErr(); err != nil {
		return util.NewErrorMsg(err)
	}
	ctx := context.Background()
	msg := goalSetMsg{sessionID: sessionID, condition: condition}
	if sessionID == "" {
		created, err := ws.CreateSession(ctx, "New Session")
		if err != nil {
			return util.NewErrorMsg(err)
		}
		msg.sessionID = created.ID
		msg.created = &created
	}
	if err := ws.AgentSetGoal(ctx, msg.sessionID, condition); err != nil {
		return util.NewErrorMsg(err)
	}
	return msg
}

func (m *UI) applyGoalSet(msg goalSetMsg) tea.Cmd {
	var cmds []tea.Cmd
	if msg.created != nil && !m.hasSession() {
		m.session = msg.created
		cmds = append(cmds, m.loadSession(msg.created.ID))
		m.setState(uiChat, m.focus)
	}
	if msg.sessionID != m.currentSessionID() {
		return util.ReportWarn("Goal was set on a session that is no longer open")
	}
	cmds = append(cmds, m.sendReadyMessage(agent.GoalPrompt(msg.condition)))
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
