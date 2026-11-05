package model

import (
	"context"

	"github.com/stubbedev/harness/internal/ui/chat"
)

// bangRun is a running bang-mode shell command: the session it runs in
// and the cancel that stops it.
type bangRun struct {
	sessionID string
	cancel    context.CancelFunc
}

// startBangRun records a bang command started in sessionID under the ID
// of its pending transcript item.
func (m *UI) startBangRun(pendingID, sessionID string, cancel context.CancelFunc) {
	if m.bangRuns == nil {
		m.bangRuns = make(map[string]bangRun)
	}
	m.bangRuns[pendingID] = bangRun{sessionID: sessionID, cancel: cancel}
}

// finishBangRun forgets the run whose command has returned, releasing its
// context.
func (m *UI) finishBangRun(pendingID string) {
	run, ok := m.bangRuns[pendingID]
	if !ok {
		return
	}
	run.cancel()
	delete(m.bangRuns, pendingID)
}

// bangRunning reports whether a bang command is running in the current
// session. Another session's command neither keeps this one busy nor
// answers its Escape.
func (m *UI) bangRunning() bool {
	sessionID := m.currentSessionID()
	for _, run := range m.bangRuns {
		if run.sessionID == sessionID {
			return true
		}
	}
	return false
}

// cancelBangRuns cancels and forgets the bang commands running in the
// current session, so the session reads idle as soon as the stop is
// pressed. Each still paints its result through its shellResultMsg.
func (m *UI) cancelBangRuns() {
	sessionID := m.currentSessionID()
	for pendingID, run := range m.bangRuns {
		if run.sessionID == sessionID {
			run.cancel()
			delete(m.bangRuns, pendingID)
		}
	}
}

// showShellResult paints a finished bang command. The pending item it
// replaces is completed wherever it still is; without one, the result
// is appended only while the transcript shows the session the command
// ran in. Any other transcript is not where it belongs, and that
// session's own transcript picks the persisted command up from history
// when it is shown again.
func (m *UI) showShellResult(msg shellResultMsg) {
	if shellItem, ok := m.chat.MessageItem(msg.PendingID).(*chat.ShellItem); ok {
		shellItem.Complete(msg.Output, msg.ExitCode)
		m.chat.ScrollToBottom()
		return
	}
	if msg.SessionID != m.currentSessionID() || m.agentView.shown != "" {
		return
	}
	m.chat.AppendMessages(chat.NewShellItem(m.com.Styles, msg.Command, msg.Output, msg.ExitCode))
	m.chat.ScrollToBottom()
}
