package model

import (
	"context"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/message"
)

// promptHistoryLoadedMsg is sent when prompt history is loaded.
type promptHistoryLoadedMsg struct {
	// forSession is the session the load was scoped to ("" for all
	// sessions); a result that raced a session switch is discarded.
	forSession string
	messages   []string
}

// loadPromptHistory loads user messages for history navigation.
func (m *UI) loadPromptHistory() tea.Cmd {
	sessionID := m.currentSessionID()
	return func() tea.Msg {
		ctx := context.Background()
		var messages []message.Message
		var err error

		if sessionID != "" {
			messages, err = m.com.Workspace.ListUserMessages(ctx, sessionID)
		} else {
			messages, err = m.com.Workspace.ListAllUserMessages(ctx)
		}
		if err != nil {
			slog.Error("Failed to load prompt history", "error", err)
			return promptHistoryLoadedMsg{forSession: sessionID}
		}

		texts := make([]string, 0, len(messages))
		for _, msg := range messages {
			if text := msg.Content().Text; text != "" {
				texts = append(texts, text)
			}
			for _, sc := range msg.ShellCommands() {
				texts = append(texts, "!"+sc.Command)
			}
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
		m.textarea.Reset()
		m.textarea.InsertString(m.promptHistory.draft)
		m.syncBangModeFromTextarea()
		return m.updateTextareaWithPrevHeight(nil, prevHeight)
	}

	// Let textarea handle escape normally.
	return m.updateTextarea(msg)
}

// updateHistoryDraft updates history state when text is modified.
func (m *UI) updateHistoryDraft(oldValue string) {
	if m.textarea.Value() != oldValue {
		m.promptHistory.draft = m.textarea.Value()
		m.promptHistory.pos = 0
	}
}

// syncBangModeFromTextarea engages or disengages bang mode based on
// whether the current textarea value starts with "!". The "!" prefix
// is stripped when entering bang mode and re-added when leaving it so
// the visible text always reflects the correct state.
func (m *UI) syncBangModeFromTextarea() {
	val := m.textarea.Value()
	hasBang := strings.HasPrefix(val, "!")
	if hasBang {
		if !m.bangMode {
			m.bangMode = true
			m.bangWasEmpty = false
		}
		m.textarea.SetValue(strings.TrimPrefix(val, "!"))
		m.textarea.MoveToBegin()
	} else if m.bangMode {
		m.bangMode = false
		m.bangWasEmpty = false
	}
	m.setEditorPrompt()
}

// historyPrev changes the text area content to the previous message in the history
// it returns false if it could not find the previous message.
func (m *UI) historyPrev() bool {
	h := &m.promptHistory
	if h.pos >= len(h.messages) {
		return false
	}
	if h.pos == 0 {
		h.draft = m.textarea.Value()
	}
	h.pos++
	m.textarea.Reset()
	m.textarea.InsertString(h.messages[h.pos-1])
	m.textarea.MoveToBegin()
	m.syncBangModeFromTextarea()
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
	m.textarea.Reset()
	if h.pos == 0 {
		m.textarea.InsertString(h.draft)
	} else {
		m.textarea.InsertString(h.messages[h.pos-1])
	}
	m.syncBangModeFromTextarea()
	return true
}

// historyReset resets the history, but does not clear the message
// it just sets the current draft to empty and the position in the history.
func (m *UI) historyReset() {
	m.promptHistory.pos = 0
	m.promptHistory.draft = ""
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

// isAtEditorTopRow returns true if the cursor is on the input's top
// display row, counting wrapped rows of the first line individually.
// There, an upward selection cannot extend any further, so shift+up
// hands focus to the region above instead.
func (m *UI) isAtEditorTopRow() bool {
	return m.textarea.Line() == 0 && m.textarea.LineInfo().RowOffset == 0
}
