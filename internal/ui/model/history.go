package model

import (
	"context"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"github.com/stubbedev/harness/internal/message"
)

// promptHistoryLoadedMsg is sent when prompt history is loaded.
type promptHistoryLoadedMsg struct {
	// forSession is the session the load was scoped to ("" for all
	// sessions); a result that raced a session switch is discarded.
	forSession string
	messages   []message.PromptHistoryEntry
}

// loadPromptHistory loads user messages for history navigation.
func (m *UI) loadPromptHistory() tea.Cmd {
	sessionID := m.currentSessionID()
	return func() tea.Msg {
		texts, err := m.com.Workspace.PromptHistory(context.Background(), sessionID)
		if err != nil {
			slog.Error("Failed to load prompt history", "error", err)
			return promptHistoryLoadedMsg{forSession: sessionID}
		}
		return promptHistoryLoadedMsg{forSession: sessionID, messages: texts}
	}
}

// handleHistoryUp handles up arrow for history navigation.
func (m *UI) handleHistoryUp(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Navigate to older history entry from cursor position (0,0).
	if m.textarea.Length() == 0 || m.isAtEditorStart() {
		if m.historyPrev() {
			// we send this so that the textarea moves the view to the correct position
			// without this the cursor will show up in the wrong place.
			return m.updateTextareaWithPrevHeight(nil, prevHeight)
		}
	}

	// First move cursor to start before entering history.
	if m.textarea.Line() == 0 {
		m.textarea.CursorStart()
		return nil
	}

	// Let textarea handle normal cursor movement.
	return m.updateTextarea(msg)
}

// handleHistoryDown handles down arrow for history navigation.
func (m *UI) handleHistoryDown(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Navigate to newer history entry from end of text.
	if m.isAtEditorEnd() {
		if m.historyNext() {
			// we send this so that the textarea moves the view to the correct position
			// without this the cursor will show up in the wrong place.
			return m.updateTextareaWithPrevHeight(nil, prevHeight)
		}
	}

	// First move cursor to end before navigating history.
	if m.textarea.Line() == max(m.textarea.LineCount()-1, 0) {
		m.textarea.MoveToEnd()
		return m.updateTextarea(nil)
	}

	// Let textarea handle normal cursor movement.
	return m.updateTextarea(msg)
}

// handleHistoryEscape handles escape for exiting history navigation.
func (m *UI) handleHistoryEscape(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Return to current draft when browsing history.
	if m.promptHistory.pos > 0 {
		m.promptHistory.pos = 0
		m.showPromptEntry(m.promptHistory.draft)
		return m.updateTextareaWithPrevHeight(nil, prevHeight)
	}

	// Let textarea handle escape normally.
	return m.updateTextarea(msg)
}

// updateHistoryDraft updates history state when text is modified.
func (m *UI) updateHistoryDraft(oldValue string) {
	if m.textarea.Value() != oldValue {
		m.promptHistory.draft = m.currentPromptEntry()
		m.promptHistory.pos = 0
	}
}

func (m *UI) currentPromptEntry() message.PromptHistoryEntry {
	return message.PromptHistoryEntry{Text: m.textarea.Value(), Shell: m.bangMode}
}

func (m *UI) showPromptEntry(entry message.PromptHistoryEntry) {
	m.textarea.Reset()
	m.textarea.InsertString(entry.Text)
	m.setShellMode(entry.Shell)
}

// historyPrev changes the text area content to the previous message in the history
// it returns false if it could not find the previous message.
func (m *UI) historyPrev() bool {
	h := &m.promptHistory
	if h.pos >= len(h.messages) {
		return false
	}
	if h.pos == 0 {
		h.draft = m.currentPromptEntry()
	}
	h.pos++
	m.showPromptEntry(h.messages[h.pos-1])
	m.textarea.MoveToBegin()
	return true
}

// historyNext changes the text area content to the next message in the history
// it returns false if it could not find the next message.
func (m *UI) historyNext() bool {
	h := &m.promptHistory
	if h.pos == 0 {
		return false
	}
	h.pos--
	if h.pos == 0 {
		m.showPromptEntry(h.draft)
	} else {
		m.showPromptEntry(h.messages[h.pos-1])
	}
	return true
}

// historyReset resets the history, but does not clear the message
// it just sets the current draft to empty and the position in the history.
func (m *UI) historyReset() {
	m.promptHistory.pos = 0
	m.promptHistory.draft = message.PromptHistoryEntry{}
}

// clearEditorDraft empties the input together with the state riding it
// — history navigation and bang mode — leaving a pristine prompt.
func (m *UI) clearEditorDraft() tea.Cmd {
	prevHeight := m.textarea.Height()
	m.textarea.Reset()
	m.historyReset()
	m.setShellMode(false)
	return m.handleTextareaHeightChange(prevHeight)
}

// isAtEditorStart returns true if we are at the 0 line and 0 col in the textarea.
func (m *UI) isAtEditorStart() bool {
	return m.textarea.Line() == 0 && m.textarea.LineInfo().ColumnOffset == 0
}

// isAtEditorEnd returns true if we are in the last line and the last column in the textarea.
func (m *UI) isAtEditorEnd() bool {
	lineCount := m.textarea.LineCount()
	if lineCount == 0 {
		return true
	}
	if m.textarea.Line() != lineCount-1 {
		return false
	}
	info := m.textarea.LineInfo()
	return info.CharOffset >= info.CharWidth-1 || info.CharWidth == 0
}
