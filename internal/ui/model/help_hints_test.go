package model

import (
	"testing"

	"charm.land/bubbles/v2/key"
	"github.com/stretchr/testify/require"
)

// helpKey finds the help key text of the binding whose description is desc.
func helpKey(t *testing.T, binds []key.Binding, desc string) string {
	t.Helper()
	for _, b := range binds {
		if b.Help().Desc == desc {
			return b.Help().Key
		}
	}
	t.Fatalf("no binding with description %q in help", desc)
	return ""
}

// TestCommandsHintDerivesFromKeymap pins that the commands hint names the
// keys the commands binding actually carries. "/" opens the skills palette,
// not commands, so the commands hint must never advertise it — and a
// keybind override must change the hint.
func TestCommandsHintDerivesFromKeymap(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	short := m.ShortHelp()
	require.Equal(t, "ctrl+p", helpKey(t, short, "commands"))
	require.Equal(t, "/", helpKey(t, short, "skills"))

	var flat []key.Binding
	for _, col := range m.FullHelp() {
		flat = append(flat, col...)
	}
	require.Equal(t, "ctrl+p", helpKey(t, flat, "commands"))
	require.Equal(t, "/", helpKey(t, flat, "skills"))

	m.keyMap.ApplyKeybinds(map[string][]string{"commands": {"ctrl+k"}})
	require.Equal(t, "ctrl+k", helpKey(t, m.ShortHelp(), "commands"))
}

// TestSkillsHintOnlyWhileEditorEmpty: "/" opens the skills palette only as
// the editor's first character, so the skills hint disappears once the
// editor holds text.
func TestSkillsHintOnlyWhileEditorEmpty(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.textarea.SetValue("partial prompt")

	for _, b := range m.ShortHelp() {
		require.NotEqual(t, "skills", b.Help().Desc)
	}
	for _, col := range m.FullHelp() {
		for _, b := range col {
			require.NotEqual(t, "skills", b.Help().Desc)
		}
	}
}
