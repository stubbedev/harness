package model

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// isAtEditorTopRow returns true if the cursor is on the input's top
// display row, counting wrapped rows of the first line individually.
// There a line move cannot go any higher, so shift+up selects to the
// input's start instead, and only from there leaves the editor.
func (m *UI) isAtEditorTopRow() bool {
	return m.textarea.Line() == 0 && m.textarea.LineInfo().RowOffset == 0
}

// isAtEditorBottomRow returns true if the cursor is on the input's last
// display row, counting wrapped rows of the last line individually.
func (m *UI) isAtEditorBottomRow() bool {
	if m.textarea.Line() != m.textarea.LineCount()-1 {
		return false
	}
	info := m.textarea.LineInfo()
	return info.RowOffset+1 >= info.Height
}

// selectToEditorEdge extends the editor's keyboard selection to the end
// of the input when forward is set, otherwise to its start. It steps the
// textarea's own character selection, so the anchor of a selection in
// progress is kept and a fresh one anchors at the cursor.
func (m *UI) selectToEditorEdge(forward bool) {
	step := editorSelectBackwardKey
	if forward {
		step = editorSelectForwardKey
	}
	for range len(m.textarea.Value()) {
		line, info := m.textarea.Line(), m.textarea.LineInfo()
		m.textarea, _ = m.textarea.Update(step)
		if m.textarea.Line() == line && m.textarea.LineInfo() == info {
			return
		}
	}
}

// Keyboard selection steps one character at a time on these keys. The
// editor's character-selection bindings are built from them and
// selectToEditorEdge steps with them, so the step always matches what
// the textarea binds.
var (
	editorSelectBackwardKey = tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift}
	editorSelectForwardKey  = tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift}
)

// bindEditorKeys points the textarea's bindings at harness's keymap.
// Every editor the UI builds goes through it.
func bindEditorKeys(ta *textarea.Model, keyMap KeyMap) {
	// Keep "ctrl+a" for line-start (the textarea default); bind select-all
	// to "ctrl+shift+a" instead (line-start is also available via "home").
	ta.KeyMap.LineStart = keyMap.Editor.LineStart
	ta.KeyMap.SelectAll = keyMap.Editor.SelectAll
	// Line selection flows through the rebindable keymap too; shift+up
	// still leaves the editor from the top row via Chat.UpOneItem.
	ta.KeyMap.SelectLineUp = keyMap.Editor.SelectLineUp
	ta.KeyMap.SelectLineDown = keyMap.Editor.SelectLineDown
	ta.KeyMap.SelectCharacterBackward = key.NewBinding(
		key.WithKeys(editorSelectBackwardKey.String()),
		key.WithHelp("shift+←", "select character backward"),
	)
	ta.KeyMap.SelectCharacterForward = key.NewBinding(
		key.WithKeys(editorSelectForwardKey.String()),
		key.WithHelp("shift+→", "select character forward"),
	)
	// Word deletion flows through the rebindable keymap like every
	// other editor binding.
	ta.KeyMap.DeleteWordBackward = keyMap.Editor.DeleteWordBackward
	// Copying is handled by harness's keymap (Editor.CopySelection) so it
	// can use harness's clipboard backend and user feedback; disable the
	// textarea's built-in copy binding.
	ta.KeyMap.CopySelection = key.NewBinding()
}
