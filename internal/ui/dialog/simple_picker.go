package dialog

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// pickerOption is one choice in a [simplePicker].
type pickerOption struct {
	value string
	title string
}

type pickerLayout struct {
	title     string
	titleInfo string
	header    string
	minHeight int
	maxHeight int
}

func drawPicker(scr uv.Screen, area uv.Rectangle, t *styles.Styles, input *textinput.Model, l *list.FilterableList, layout pickerLayout) *tea.Cursor {
	width := DialogWidth(t, area)
	innerWidth := DialogInnerWidth(t, width)

	input.SetWidth(dialogInputTextWidth(t, *input, innerWidth))

	headerHeight := 0
	if layout.header != "" {
		headerHeight = lipgloss.Height(layout.header)
	}
	height := pickerHeight(t, area, layout.minHeight, layout.maxHeight, l.TotalHeight()+headerHeight)
	listHeight, listTotalHeight, _ := sizeDialogList(t, l, innerWidth, height-headerHeight)

	rc := NewRenderContext(t, width)
	rc.Title = layout.title
	rc.TitleInfo = layout.titleInfo
	rc.AddInput(input.View())
	rc.AddPart(layout.header)
	if listHeight > 0 {
		if listHeight >= listTotalHeight {
			l.ScrollToTop()
		} else {
			l.ScrollToSelected()
		}
		listView := t.Dialog.List.Height(listHeight).Render(l.Render())
		rc.AddPart(joinScrollbar(t, listView, listHeight, listTotalHeight, listHeight, l.Offset()))
	}

	view := rc.Render()
	cur := DialogCursor(t, view, input.Cursor())
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

type simplePicker[T any] struct {
	com   *common.Common
	list  *list.FilterableList
	input textinput.Model

	id        ID
	title     string
	minHeight int
	maxHeight int
	onSelect  func(value T) Action
	onMove    func(cmd tea.Cmd) Action

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*simplePicker[string])(nil)

func newSimplePicker[T any](
	com *common.Common,
	id ID,
	title string,
	minHeight, maxHeight int,
	onSelect func(value T) Action,
	onMove func(cmd tea.Cmd) Action,
) *simplePicker[T] {
	p := &simplePicker[T]{
		com:       com,
		id:        id,
		title:     title,
		minHeight: minHeight,
		maxHeight: maxHeight,
		onSelect:  onSelect,
		onMove:    onMove,
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
	return p
}

func carryCmd(cmd tea.Cmd) Action {
	return ActionCmd{cmd}
}

func newOptionPicker(
	com *common.Common,
	id ID,
	title string,
	minHeight, maxHeight int,
	options []pickerOption,
	current string,
	onSelect func(value string) Action,
) *simplePicker[string] {
	p := newSimplePicker(com, id, title, minHeight, maxHeight, onSelect, carryCmd)
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
func (p *simplePicker[T]) ID() ID {
	return p.id
}

func (p *simplePicker[T]) selected() (T, bool) {
	var zero T
	item, ok := p.list.SelectedItem().(PickerItem)
	if !ok || item == nil {
		return zero, false
	}
	value, ok := item.Value().(T)
	return value, ok
}

// HandleMsg implements [Dialog].
func (p *simplePicker[T]) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, p.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, p.keyMap.Previous):
			p.list.Focus()
			selectPrevWrap(p.list)
			return p.onMove(nil)
		case key.Matches(msg, p.keyMap.Next):
			p.list.Focus()
			selectNextWrap(p.list)
			return p.onMove(nil)
		case key.Matches(msg, p.keyMap.Select):
			if value, ok := p.selected(); ok {
				return p.onSelect(value)
			}
		default:
			cmd, changed := filterInput(&p.input, msg, applyListFilter(p.list))
			if changed {
				return p.onMove(cmd)
			}
			return ActionCmd{cmd}
		}
	}
	return nil
}

// Draw implements [Dialog].
func (p *simplePicker[T]) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	return drawPicker(scr, area, p.com.Styles, &p.input, p.list, pickerLayout{
		title:     p.title,
		minHeight: p.minHeight,
		maxHeight: p.maxHeight,
	})
}

// ShortHelp implements [help.KeyMap].
func (p *simplePicker[T]) ShortHelp() []key.Binding {
	return []key.Binding{
		p.keyMap.UpDown,
		p.keyMap.Select,
		p.keyMap.Close,
	}
}

// FullHelp implements [help.KeyMap].
func (p *simplePicker[T]) FullHelp() [][]key.Binding {
	return [][]key.Binding{{
		p.keyMap.Select,
		p.keyMap.Next,
		p.keyMap.Previous,
		p.keyMap.Close,
	}}
}
