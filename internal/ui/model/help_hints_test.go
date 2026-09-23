package model

import (
	"slices"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/message"
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

// hasDesc reports whether any binding in binds carries the description.
func hasDesc(binds []key.Binding, desc string) bool {
	for _, b := range binds {
		if b.Help().Desc == desc {
			return true
		}
	}
	return false
}

// flatHelp flattens the full help columns into one slice.
func flatHelp(cols [][]key.Binding) []key.Binding {
	var out []key.Binding
	for _, col := range cols {
		out = append(out, col...)
	}
	return out
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
	require.Equal(t, ":", helpKey(t, short, "command palette"))
	require.Equal(t, "/", helpKey(t, short, "skills"))

	var flat []key.Binding
	for _, col := range m.FullHelp() {
		flat = append(flat, col...)
	}
	require.Equal(t, "ctrl+p", helpKey(t, flat, "commands"))
	require.Equal(t, ":", helpKey(t, flat, "command palette"))
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
		require.NotEqual(t, "command palette", b.Help().Desc)
	}
	for _, col := range m.FullHelp() {
		for _, b := range col {
			require.NotEqual(t, "skills", b.Help().Desc)
			require.NotEqual(t, "command palette", b.Help().Desc)
		}
	}
}

// TestShellModeHintOnlyWhileEditorIdle pins the "!" hint: it is shown
// while the editor is empty and idle, and hidden once the editor holds
// text or bang mode is already active — the states where pressing it
// would type a character instead of entering shell mode.
func TestShellModeHintOnlyWhileEditorIdle(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	require.Equal(t, "!", helpKey(t, m.ShortHelp(), "shell mode"))

	m.textarea.SetValue("partial prompt")
	require.False(t, hasDesc(m.ShortHelp(), "shell mode"))

	m.textarea.Reset()
	m.bangMode = true
	require.False(t, hasDesc(m.ShortHelp(), "shell mode"))
}

// TestNewlineHintNamesShiftEnter pins the newline hint to the key users
// actually press; ctrl+j is only the fallback for terminals that cannot
// send shift+enter.
func TestNewlineHintNamesShiftEnter(t *testing.T) {
	require.Equal(t, "shift+enter", DefaultKeyMap().Editor.Newline.Help().Key)
}

// TestAttachmentHintsFollowDeleteMode pins the attachment hints to the
// live routing: ctrl+r is advertised whenever attachments exist, and once
// delete mode is armed the advertised keys are the ones that still work —
// esc leaves the mode and r clears every attachment, except where an
// idle rewind or a busy cancel would consume esc first.
func TestAttachmentHintsFollowDeleteMode(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	m.attachments.Update(message.Attachment{FileName: "a.txt"})
	require.Equal(t, "ctrl+r+{i}", helpKey(t, m.ShortHelp(), "delete attachment at index i"))
	require.False(t, hasDesc(flatHelp(m.FullHelp()), "cancel delete mode"))
	require.False(t, hasDesc(flatHelp(m.FullHelp()), "delete all attachments"))

	// Armed: ctrl+r is spent; esc and r own the moment.
	m.attachments.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	require.False(t, hasDesc(m.ShortHelp(), "delete attachment at index i"))
	require.Equal(t, "esc", helpKey(t, m.ShortHelp(), "cancel delete mode"))
	require.Equal(t, "ctrl+r+r", helpKey(t, m.ShortHelp(), "delete all attachments"))

	// Armed and busy: esc cancels the run (cancel is checked before
	// delete mode), so the delete-mode esc hint yields.
	warmCaches(m, true)
	require.True(t, hasDesc(m.ShortHelp(), "cancel"))
	require.False(t, hasDesc(m.ShortHelp(), "cancel delete mode"))
	require.True(t, hasDesc(m.ShortHelp(), "delete all attachments"))

	// Armed with rewind armed: esc leaves delete mode first, so the
	// rewind hint yields instead.
	warmCaches(m, false)
	m.esc.set(escRewind)
	require.False(t, hasDesc(m.ShortHelp(), "press again to rewind"))
	require.True(t, hasDesc(m.ShortHelp(), "cancel delete mode"))
}

// TestDetailsHintOnHelpRowOnlyWithSession pins the ctrl+d hint to the
// bottom help row, shown only in a chat session where the key routes;
// the details dialog declares its own close/toggle hints while open.
func TestDetailsHintOnHelpRowOnlyWithSession(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	require.True(t, hasDesc(m.ShortHelp(), "toggle details"))
	require.True(t, hasDesc(flatHelp(m.FullHelp()), "toggle details"))

	m.session = nil
	require.False(t, hasDesc(m.ShortHelp(), "toggle details"))
	require.False(t, hasDesc(flatHelp(m.FullHelp()), "toggle details"))
}

// stubInline is a minimal InlineEditor standing in for the question form.
type stubInline struct {
	help []key.Binding
}

func (s *stubInline) HandleKey(tea.KeyPressMsg) (bool, tea.Cmd) { return false, nil }
func (s *stubInline) ShortHelp() []key.Binding                  { return s.help }
func (s *stubInline) Height(int) int                            { return 3 }
func (s *stubInline) Draw(uv.Screen, uv.Rectangle) *tea.Cursor  { return nil }
func (s *stubInline) HeightChanged() bool                       { return false }
func (s *stubInline) SetFocused(bool)                           {}

// TestInlineHelpOnlyWhileFocused pins the inline editor's hints to the
// state where its keys are routed: tab moves focus to the chat, the form
// stops handling keys, and the hints must follow.
func TestInlineHelpOnlyWhileFocused(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.activeInline = &stubInline{help: []key.Binding{
		key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "stub action")),
	}}

	m.focus = uiFocusEditor
	short := m.ShortHelp()
	require.Len(t, short, 1)
	require.Equal(t, "stub action", short[0].Help().Desc)
	require.Len(t, m.FullHelp(), 1)

	m.focus = uiFocusMain
	short = m.ShortHelp()
	require.False(t, hasDesc(short, "stub action"))
	require.True(t, slices.ContainsFunc(short, func(b key.Binding) bool {
		return b.Help().Desc == "commands"
	}))
}
