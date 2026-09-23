package dialog

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
)

// pickerOption is one choice in a [simplePicker].
type pickerOption struct {
	value string
	title string
}

// simplePicker is a filterable single-choice dialog over a short fixed
// list of string values: it sizes to its content between minHeight and
// maxHeight and turns the chosen value into an action via onSelect.
type simplePicker struct {
	com   *common.Common
	list  *list.FilterableList
	input textinput.Model

	id        ID
	title     string
	minHeight int
	maxHeight int
	onSelect  func(value string) Action

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*simplePicker)(nil)

// newSimplePicker builds a picker over options with current preselected
// (the first option when current is not among them).
func newSimplePicker(
	com *common.Common,
	id ID,
	title string,
	minHeight, maxHeight int,
	options []pickerOption,
	current string,
	onSelect func(value string) Action,
) *simplePicker {
	p := &simplePicker{
		com:       com,
		id:        id,
		title:     title,
		minHeight: minHeight,
		maxHeight: maxHeight,
		onSelect:  onSelect,
	}

	p.list = list.NewFilterableList()
	p.list.Focus()

	p.input = textinput.New()
	p.input.SetVirtualCursor(false)
	p.input.Placeholder = "Type to filter"
	p.input.SetStyles(com.Styles.TextInput)
	p.input.Focus()

	km := dialogKeys()
	p.keyMap.Select = km.Select
	p.keyMap.Next = km.Next
	p.keyMap.Previous = km.Previous
	p.keyMap.UpDown = km.UpDown
	p.keyMap.Close = km.Close

	items := make([]list.FilterableItem, 0, len(options))
	selected := 0
	for i, opt := range options {
		items = append(items, NewPickerItem(com.Styles, opt.value, opt.title, ""))
		if opt.value == current {
			selected = i
		}
	}
	p.list.SetItems(items...)
	p.list.SetSelected(selected)
	p.list.ScrollToSelected()
	return p
}

// ID implements [Dialog].
func (p *simplePicker) ID() ID {
	return p.id
}

// HandleMsg implements [Dialog].
func (p *simplePicker) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, p.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, p.keyMap.Previous):
			p.list.Focus()
			selectPrevWrap(p.list)
		case key.Matches(msg, p.keyMap.Next):
			p.list.Focus()
			selectNextWrap(p.list)
		case key.Matches(msg, p.keyMap.Select):
			if item, ok := p.list.SelectedItem().(PickerItem); ok && item != nil {
				if value, ok := item.Value().(string); ok {
					return p.onSelect(value)
				}
			}
		default:
			cmd, _ := filterInput(&p.input, msg, applyListFilter(p.list))
			return ActionCmd{cmd}
		}
	}
	return nil
}

// Cursor returns the cursor position relative to the dialog.
func (p *simplePicker) Cursor() *tea.Cursor {
	return InputCursor(p.com.Styles, p.input.Cursor())
}

// Draw implements [Dialog].
func (p *simplePicker) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := p.com.Styles
	width := DialogWidth(t, area)
	innerWidth := DialogInnerWidth(t, width)

	p.input.SetWidth(dialogInputTextWidth(t, p.input, innerWidth))

	// Size the dialog to fit the list content, clamped to min/max bounds.
	heightOffset := dialogChromeHeight(t, t.Dialog.HelpView)
	desiredHeight := heightOffset + p.list.TotalHeight()
	maxAvailable := DialogHeightCeiling(t, area, p.maxHeight)
	height := max(p.minHeight, min(p.maxHeight, desiredHeight, maxAvailable))

	listHeight, listTotalHeight, _ := sizeDialogList(t, p.list, innerWidth, height)

	rc := NewRenderContext(t, width)
	rc.Title = p.title
	rc.AddInput(p.input.View())

	if p.list.Height() >= len(p.list.FilteredItems()) {
		p.list.ScrollToTop()
	} else {
		p.list.ScrollToSelected()
	}

	listView := t.Dialog.List.Height(p.list.Height()).Render(p.list.Render())
	listView = joinScrollbar(t, listView, listHeight, listTotalHeight, listHeight, p.list.Offset())
	rc.AddPart(listView)

	view := rc.Render()

	cur := DialogCursor(t, view, p.input.Cursor())
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (p *simplePicker) ShortHelp() []key.Binding {
	return []key.Binding{
		p.keyMap.UpDown,
		p.keyMap.Select,
		p.keyMap.Close,
	}
}

// FullHelp implements [help.KeyMap].
func (p *simplePicker) FullHelp() [][]key.Binding {
	return [][]key.Binding{{
		p.keyMap.Select,
		p.keyMap.Next,
		p.keyMap.Previous,
		p.keyMap.Close,
	}}
}
