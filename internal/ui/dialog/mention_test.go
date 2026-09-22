package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/ui/completions"
)

// newMentionPickerForTest builds a picker pre-loaded with a small file
// set, so tests exercise selection without the async loaders.
func newMentionPickerForTest(t *testing.T) *MentionPicker {
	t.Helper()
	p, _ := NewMentionPicker(newMentionTestCommon(), nil, 0, 0)
	p.HandleMsg(completions.CompletionItemsLoadedMsg{
		Files: []completions.FileCompletionValue{
			{Path: "internal/ui/model/landing.go"},
			{Path: "internal/ui/chat/mcp.go"},
		},
	})
	return p
}

// TestMentionPickerFilterRanksNamePriority pins the ranking through the
// real typing path: a basename match surfaces above a deeper path
// match.
func TestMentionPickerFilterRanksNamePriority(t *testing.T) {
	t.Parallel()

	p := newMentionPickerForTest(t)
	for _, r := range "mcp" {
		p.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	row, ok := p.list.SelectedItem().(PickerItem)
	require.True(t, ok)
	require.NotNil(t, row)
	require.Equal(t, "internal/ui/chat/mcp.go", row.Label())
}

// TestMentionPickerEscapeCancels pins that closing the picker reports a
// cancellation (so the model can remove the typed "@"), not a plain
// close.
func TestMentionPickerEscapeCancels(t *testing.T) {
	t.Parallel()

	p := newMentionPickerForTest(t)
	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.IsType(t, ActionMentionCancelled{}, action)
}

// TestMentionPickerRowsUseDialogItemStyles pins the single render
// source: rows carry no background of their own; only the focused row
// takes the shared selection background.
func TestMentionPickerRowsUseDialogItemStyles(t *testing.T) {
	t.Parallel()

	p := newMentionPickerForTest(t)
	row := p.list.FilteredItems()[0].(PickerItem)

	row.SetFocused(false)
	require.NotContains(t, row.Render(60), "\x1b[48", "a normal row must not paint a background")

	row.SetFocused(true)
	require.Contains(t, row.Render(60), "\x1b[48", "the focused row takes the shared selection background")
}

// TestMentionPickerItemsOrder pins the source order: subagents first,
// then files, then MCP resources, each kind labeled on the right.
func TestMentionPickerItemsOrder(t *testing.T) {
	t.Parallel()

	st := newMentionTestCommon().Styles
	items := mentionPickerItems(st, completions.CompletionItemsLoadedMsg{
		Files:     []completions.FileCompletionValue{{Path: "a.go"}},
		Resources: []completions.ResourceCompletionValue{{MCPName: "m", URI: "u", Title: "t"}},
		Subagents: []completions.SubagentCompletionValue{{Name: "zeta"}},
	})

	labels := make([]string, len(items))
	for i, item := range items {
		row := item.(PickerItem)
		labels[i] = row.Label()
	}
	require.Equal(t, []string{"zeta", "a.go", "m/t"}, labels)
}
