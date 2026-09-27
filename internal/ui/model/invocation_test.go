package model

import (
	"image"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestPickerDrawsAboveEditor pins what the picker shows: each command
// under the prefix it is typed with, its argument hint beside it.
func TestPickerDrawsAboveEditor(t *testing.T) {
	t.Parallel()

	m, _ := newCompactUI(t)
	typeText(m, "/comp")
	require.True(t, m.commandPicker.visible())

	scr := uv.NewScreenBuffer(80, 20)
	m.drawCommandPicker(scr, image.Rect(0, 19, 80, 20))
	view := ansi.Strip(scr.Render())
	require.Contains(t, view, "/compact")
	require.Contains(t, view, "[focus]")
}

// TestPickerEscDismissesUntilTextChanges pins esc: it hides the picker
// for the text it was pressed on, and the next edit brings it back.
func TestPickerEscDismissesUntilTextChanges(t *testing.T) {
	t.Parallel()

	m, _ := newCompactUI(t)
	typeText(m, "/comp")
	pressKey(t, m, tea.KeyEscape)
	require.False(t, m.commandPicker.visible())
	require.Equal(t, "/comp", m.textarea.Value(), "esc leaves the text alone")

	typeText(m, "a")
	require.True(t, m.commandPicker.visible())
}

// TestPickerTabCompletes pins tab: it fills in the selected name and a
// space, whatever the command takes, and the picker gives way to the
// arguments.
func TestPickerTabCompletes(t *testing.T) {
	t.Parallel()

	m, _ := newCompactUI(t)
	typeText(m, "/summ")
	pressKey(t, m, tea.KeyTab)

	require.Equal(t, "/summarize ", m.textarea.Value())
	require.False(t, m.commandPicker.visible())
}

// TestPickerRanksPrefixFirst pins the ranking: a name the text starts,
// then an alias it starts, then fuzzy matches.
func TestPickerRanksPrefixFirst(t *testing.T) {
	t.Parallel()

	m, _ := newCompactUI(t)
	typeText(m, "/cl")
	require.True(t, m.commandPicker.visible())
	first := m.commandPicker.matches[0]
	require.True(t, slices.ContainsFunc(first.SlashNames(), func(n string) bool { return strings.HasPrefix(n, "cl") }),
		"got %v first", first.SlashNames())
}
