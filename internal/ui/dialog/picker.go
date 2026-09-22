package dialog

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// PickerItem is one row of a picker dialog list: a backing value, its
// label, and an optional right-aligned label. Every picker builds its
// rows through this interface, so every picker renders identically -
// transparent normal rows, the shared selection background only on the
// focused row, fuzzy matches underlined.
type PickerItem interface {
	list.FilterableItem
	list.MatchSettable
	list.Focusable

	// Value is the payload the picker resolves a selection to; pickers
	// type-switch on it in their confirm handlers.
	Value() any
	// Label is the row's main text and its default filter text.
	Label() string
	// RightLabel is the row's optional right-aligned info text; empty
	// renders nothing.
	RightLabel() string
}

// NewPickerItem builds the standard picker row. filterText optionally
// widens what fuzzy matching sees (e.g. a command's aliases) beyond the
// label; the label always renders.
func NewPickerItem(t *styles.Styles, value any, label, right string, filterText ...string) PickerItem {
	filter := label
	if len(filterText) > 0 && filterText[0] != "" {
		filter = filterText[0]
	}
	return &pickerRow{
		Versioned: list.NewVersioned(),
		t:         t,
		value:     value,
		label:     label,
		right:     right,
		filter:    filter,
	}
}

// pickerRow is the standard PickerItem implementation. It renders
// through the shared row path (pickerItemStyles + renderItem), so a
// picker cannot style its rows differently from every other picker.
type pickerRow struct {
	*list.Versioned

	t       *styles.Styles
	value   any
	label   string
	right   string
	filter  string
	match   fuzzy.Match
	focused bool
	cache   map[int]string
}

// Value implements PickerItem.
func (p *pickerRow) Value() any { return p.value }

// Label implements PickerItem.
func (p *pickerRow) Label() string { return p.label }

// RightLabel implements PickerItem.
func (p *pickerRow) RightLabel() string { return p.right }

// Filter implements list.FilterableItem.
func (p *pickerRow) Filter() string { return p.filter }

// Finished implements list.Item: rows render purely from immutable
// content plus (match, focus) state, which bump the version.
func (p *pickerRow) Finished() bool { return true }

// SetMatch implements list.MatchSettable.
func (p *pickerRow) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(p.match, m) {
		return
	}
	p.cache = nil
	p.match = m
	p.Bump()
}

// SetFocused implements list.Focusable.
func (p *pickerRow) SetFocused(focused bool) {
	if p.focused == focused {
		return
	}
	p.cache = nil
	p.focused = focused
	p.Bump()
}

// Render implements list.Item through the one shared row path.
func (p *pickerRow) Render(width int) string {
	return renderItem(pickerItemStyles(p.t), p.label, p.right, p.focused, width, p.cache, &p.match)
}

// pickerItemStyles is the single ListItemStyles assembly every picker
// row renders with; dialogs must not assemble their own.
func pickerItemStyles(t *styles.Styles) ListItemStyles {
	return ListItemStyles{
		ItemBlurred:     t.Dialog.NormalItem,
		ItemFocused:     t.Dialog.SelectedItem,
		InfoTextBlurred: t.Dialog.ListItem.InfoBlurred,
		InfoTextFocused: t.Dialog.ListItem.InfoFocused,
	}
}

// filterInput routes a key press to a picker's filter input and hands
// the new value to apply when it changed. The input half of every
// picker's typing path: only the search - the apply closure - differs
// between pickers (plain fuzzy, a custom ranking, a grouped list).
func filterInput(input *textinput.Model, msg tea.KeyPressMsg, apply func(query string)) (tea.Cmd, bool) {
	prev := input.Value()
	var cmd tea.Cmd
	*input, cmd = input.Update(msg)
	if v := input.Value(); v != prev {
		apply(v)
		return cmd, true
	}
	return cmd, false
}

// applyListFilter is the standard apply closure for filterInput: refocus
// the list, filter it and select from the top. Pickers with their own
// search (the mention ranking, the grouped models list) pass their own.
func applyListFilter(l *list.FilterableList) func(query string) {
	return func(query string) {
		l.Focus()
		l.SetFilter(query)
		l.SelectFirst()
		l.ScrollToTop()
	}
}
