package model

import (
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

var escPressWindow = 700 * time.Millisecond

const (
	escPressesToStop   = 2
	escPressesToRewind = 3
)

type escWindowExpiredMsg struct{ gen uint64 }

func (m *UI) armEsc(presses int) tea.Cmd {
	gen := m.esc.set(presses)
	return tea.Tick(escPressWindow, func(time.Time) tea.Msg {
		return escWindowExpiredMsg{gen: gen}
	})
}

func (m *UI) escapeRouted() bool {
	return m.focus == uiFocusEditor && m.hasSession()
}

func (m *UI) editorOwnsEscape() bool {
	return m.promptHistory.pos > 0 || m.textarea.Value() != ""
}

func (m *UI) canStop() bool {
	return m.isAgentBusy() || len(m.pendingQueuedPrompts()) > 0 || len(m.runningSubagents) > 0
}

func (m *UI) pendingQueuedPrompts() []string {
	if len(m.queuedPrompts) > 0 && m.queuedPromptsSession == m.currentSessionID() {
		return m.queuedPrompts
	}
	return m.promptQueueItems
}

func (m *UI) handleEscape() (bool, tea.Cmd) {
	if !m.escapeRouted() {
		m.esc.clear()
		return false, nil
	}
	presses := m.esc.state + 1
	if presses == 1 && !m.canStop() && m.editorOwnsEscape() {
		m.esc.clear()
		return false, nil
	}
	switch presses {
	case escPressesToStop:
		return true, tea.Batch(m.stopAgent(), m.armEsc(presses))
	case escPressesToRewind:
		m.esc.clear()
		return true, m.openRewindDialog()
	}
	return true, m.armEsc(presses)
}

func (m *UI) stopAgent() tea.Cmd {
	if !m.canStop() {
		return nil
	}
	if m.bangCancel != nil {
		m.bangCancel()
		m.bangCancel = nil
	}
	dropped := slices.Clone(m.pendingQueuedPrompts())
	m.reconcileQueuedPrompts(nil)
	m.promptQueueItems = nil
	m.invalidatePromptQueue()
	m.agentBusyCache.set(false)
	m.busyFetchGen++
	m.stopping = true
	m.chat.SetAnimationsAllowed(false)
	m.updateLayoutAndSize()

	done := make(chan struct{})
	m.stopDone = done
	return tea.Batch(m.restoreStoppedPrompts(dropped), cancelSession(m.com.Workspace, m.session.ID, done))
}

func cancelSession(ws workspace.Workspace, sessionID string, done chan struct{}) tea.Cmd {
	return func() tea.Msg {
		defer close(done)
		if err := ws.AgentCancel(sessionID); err != nil {
			return util.NewErrorMsg(err)
		}
		return nil
	}
}

func awaitStop(done <-chan struct{}) {
	if done != nil {
		<-done
	}
}

func (m *UI) settleStop(busy bool) bool {
	if !m.stopping {
		return busy
	}
	if !busy {
		m.stopping = false
	}
	return false
}

func (m *UI) restoreStoppedPrompts(prompts []string) tea.Cmd {
	if len(prompts) == 0 {
		return nil
	}
	prevHeight := m.textarea.Height()
	text := strings.Join(prompts, "\n\n")
	if draft := m.textarea.Value(); draft != "" {
		text = draft + "\n\n" + text
	}
	m.textarea.SetValue(text)
	m.textarea.MoveToEnd()
	return m.updateTextareaWithPrevHeight(nil, prevHeight)
}

func (m *UI) escHintBinding() (key.Binding, bool) {
	if !m.escapeRouted() {
		return key.Binding{}, false
	}
	b := m.keyMap.Chat.Cancel
	next := m.esc.state + 1
	switch {
	case next == escPressesToRewind:
		b.SetHelp(keys.HelpKeys(b), "press again to rewind")
	case next == escPressesToStop && m.canStop():
		b.SetHelp(keys.HelpKeys(b), "press again to stop")
	case next == escPressesToStop:
		b.SetHelp(keys.HelpKeys(b), "press twice more to rewind")
	case !m.canStop():
		return key.Binding{}, false
	}
	return b, true
}
