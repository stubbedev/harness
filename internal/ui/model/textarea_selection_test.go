package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

func newSelectionTestUI() *UI {
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.updateLayoutAndSize()
	return u
}

func TestTextareaSelectionKeys(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()

	for _, r := range "hello" {
		u.textarea.InsertRune(r)
	}
	u.textarea.CursorStart()

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})

	require.True(t, u.textarea.HasSelection())
	require.Equal(t, "he", u.textarea.SelectedText())
}

func TestTextareaCutSelection(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()
	u.keyMap = DefaultKeyMap()

	for _, r := range "hello" {
		u.textarea.InsertRune(r)
	}
	u.textarea.CursorStart()

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	require.True(t, u.textarea.HasSelection())

	_, _ = u.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl | tea.ModShift})

	require.False(t, u.textarea.HasSelection())
	require.Equal(t, "llo", u.textarea.Value())
}

// TestShiftUpSelectsUntilTopEdge pins the editor's shift+up contract:
// while display rows remain above the cursor the key extends the
// selection in place, on the top row it grows the selection to the start
// of the input, and only from there, where the selection cannot grow,
// does it hand focus to the region above.
func TestShiftUpSelectsUntilTopEdge(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()
	u.keyMap = DefaultKeyMap()
	u.textarea.InsertString("hello\nworld")

	_, _ = u.Update(shiftUp())
	require.Equal(t, uiFocusEditor, u.focus, "shift+up with rows above must keep editing")
	require.True(t, u.textarea.HasSelection())
	require.Contains(t, u.textarea.SelectedText(), "world")

	// The cursor is now on the top row; the key selects to the start.
	_, _ = u.Update(shiftUp())
	require.Equal(t, uiFocusEditor, u.focus, "shift+up short of the start must keep editing")
	require.Equal(t, "hello\nworld", u.textarea.SelectedText())

	// At the start the selection cannot grow; the same key leaves.
	_, _ = u.Update(shiftUp())
	require.Equal(t, uiFocusMain, u.focus, "shift+up at the start must leave the editor")
}

// TestShiftUpSelectsSingleLineBackward pins that a single-line input,
// whose cursor is always on the top row, can be selected bottom to top:
// shift+up selects back to the start before it leaves the editor.
func TestShiftUpSelectsSingleLineBackward(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()
	u.keyMap = DefaultKeyMap()
	u.textarea.InsertString("hello")

	_, _ = u.Update(shiftUp())
	require.Equal(t, uiFocusEditor, u.focus)
	require.Equal(t, "hello", u.textarea.SelectedText())

	_, _ = u.Update(shiftUp())
	require.Equal(t, uiFocusMain, u.focus)
}

// TestShiftUpFromEmptyInputLeavesEditor pins that an empty input, which
// has nothing to select, keeps the direct gesture out of the editor.
func TestShiftUpFromEmptyInputLeavesEditor(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()
	u.keyMap = DefaultKeyMap()

	_, _ = u.Update(shiftUp())
	require.Equal(t, uiFocusMain, u.focus)
}

// TestShiftDownSelectsToEnd pins that shift+down on the bottom row grows
// the selection through the last rune of the input.
func TestShiftDownSelectsToEnd(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()
	u.keyMap = DefaultKeyMap()
	u.textarea.InsertString("hello\nworld")
	u.textarea.MoveToBegin()

	_, _ = u.Update(shiftDown())
	_, _ = u.Update(shiftDown())
	require.Equal(t, uiFocusEditor, u.focus)
	require.Equal(t, "hello\nworld", u.textarea.SelectedText())
}

func TestTextareaMouseSelection(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()

	for _, r := range "hello world" {
		u.textarea.InsertRune(r)
	}
	u.textarea.CursorStart()

	// Click at the textarea's own first row, at the cell after the
	// default prompt ("┃ " is 2 cells wide); the origin comes from the
	// same helper the mouse path uses, so the test cannot drift from
	// the real geometry.
	origin := u.editorContentOrigin()
	startX := origin.X + 2
	y := origin.Y

	_, _ = u.Update(tea.MouseClickMsg(tea.Mouse{X: startX, Y: y, Button: uv.MouseLeft}))
	require.True(t, u.textareaMouseSelecting)

	// Drag a few cells to the right.
	_, _ = u.Update(tea.MouseMotionMsg(tea.Mouse{X: startX + 5, Y: y, Button: uv.MouseLeft}))

	// Release to end the gesture.
	_, _ = u.Update(tea.MouseReleaseMsg(tea.Mouse{X: startX + 5, Y: y, Button: uv.MouseLeft}))
	require.False(t, u.textareaMouseSelecting)

	require.True(t, u.textarea.HasSelection())
	require.Equal(t, "hello", u.textarea.SelectedText())
}

func TestTextareaMouseClickOutsideDoesNotSelect(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()

	for _, r := range "hello" {
		u.textarea.InsertRune(r)
	}

	// Click in the chat area, far from the editor.
	_, _ = u.Update(tea.MouseClickMsg(tea.Mouse{
		X:      u.layout.main.Min.X + 1,
		Y:      u.layout.main.Min.Y + 1,
		Button: uv.MouseLeft,
	}))

	require.False(t, u.textareaMouseSelecting)
	require.False(t, u.textarea.HasSelection())
}
