package model

import (
	"slices"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
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

// TestCommandsHintDerivesFromKeymap pins that the commands hints name the
// keys the bindings actually carry, and that the two ways to a command
// are never hinted together: while the editor is empty the first
// characters ("!", "/") stand in for the global chord, and once the
// editor holds text the chord returns. A keybind override must change
// the hint.
func TestCommandsHintDerivesFromKeymap(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	short := m.ShortHelp()
	require.Equal(t, "/", helpKey(t, short, "run command"))
	require.False(t, hasDesc(short, "commands"), "the global chord must not repeat the palette hint")

	flat := flatHelp(m.FullHelp())
	require.Equal(t, "/", helpKey(t, flat, "run command"))
	require.False(t, hasDesc(flat, "commands"))

	m.textarea.SetValue("partial prompt")
	short = m.ShortHelp()
	require.Equal(t, "ctrl+p", helpKey(t, short, "commands"))
	require.False(t, hasDesc(short, "run command"))

	m.keyMap.ApplyKeybinds(map[string][]string{"commands": {"ctrl+k"}})
	require.Equal(t, "ctrl+k", helpKey(t, m.ShortHelp(), "commands"))
}

// TestInvocationHintOnlyWhileEditorEmpty: "/" starts a command only as the
// editor's first character, so its hint disappears once the editor holds
// text.
func TestInvocationHintOnlyWhileEditorEmpty(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.textarea.SetValue("partial prompt")

	for _, b := range m.ShortHelp() {
		require.NotEqual(t, "run command", b.Help().Desc)
	}
	for _, col := range m.FullHelp() {
		for _, b := range col {
			require.NotEqual(t, "run command", b.Help().Desc)
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

// TestLandingNewlineHintOnce pins the landing screen's newline hint: it
// is shown once while the editor is focused, and not at all otherwise.
func TestLandingNewlineHintOnce(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	m.state = uiLanding
	warmCaches(m, false)

	count := func() int {
		n := 0
		for _, b := range m.ShortHelp() {
			if b.Help().Desc == m.keyMap.Editor.Newline.Help().Desc {
				n++
			}
		}
		return n
	}
	require.Equal(t, 1, count())

	m.focus = uiFocusMain
	require.Equal(t, 0, count())
}
