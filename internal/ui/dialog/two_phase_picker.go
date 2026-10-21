package dialog

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
)

const twoPhasePickerMaxHeight = 24

type pickerPhase uint8

const (
	pickerPhaseList pickerPhase = iota
	pickerPhaseDetail
)

type twoPhaseOwner interface {
	confirmSelection() Action
	enterListPhase()
}

type twoPhasePicker struct {
	com   *common.Common
	list  *list.FilterableList
	input textinput.Model
	phase pickerPhase

	keyMap struct {
		Select   key.Binding
		Back     key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

func newTwoPhasePicker(com *common.Common, placeholder string, selectKey, backKey, closeKey key.Binding) twoPhasePicker {
	km := dialogKeys()
	p := twoPhasePicker{com: com, phase: pickerPhaseList}
	p.keyMap.Select = selectKey
	p.keyMap.Back = backKey
	p.keyMap.Next = keys.WithDesc(km.Next, "next")
	p.keyMap.Previous = keys.WithDesc(km.Previous, "previous")
	p.keyMap.UpDown = km.UpDown
	p.keyMap.Close = closeKey

	p.list = list.NewFilterableList()
	p.list.Focus()

	p.input = textinput.New()
	p.input.SetVirtualCursor(false)
	p.input.Prompt = "❯ "
	p.input.Placeholder = placeholder
	p.input.SetStyles(com.Styles.TextInput)
	p.input.Focus()
	return p
}

func (p *twoPhasePicker) handleMsg(msg tea.Msg, owner twoPhaseOwner) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, p.keyMap.Close):
			if p.phase == pickerPhaseDetail {
				owner.enterListPhase()
				return nil
			}
			return ActionClose{}
		case key.Matches(msg, p.keyMap.Back) && p.phase == pickerPhaseDetail:
			owner.enterListPhase()
			return nil
		case key.Matches(msg, p.keyMap.Previous):
			selectPrevWrap(p.list)
		case key.Matches(msg, p.keyMap.Next):
			selectNextWrap(p.list)
		case key.Matches(msg, p.keyMap.Select):
			return owner.confirmSelection()
		default:
			cmd, _ := filterInput(&p.input, msg, applyListFilter(p.list))
			return ActionCmd{cmd}
		}
	}
	return nil
}

func (p *twoPhasePicker) draw(scr uv.Screen, area uv.Rectangle, title, header string) *tea.Cursor {
	return drawPicker(scr, area, p.com.Styles, &p.input, p.list, pickerLayout{
		title:     title,
		header:    header,
		maxHeight: twoPhasePickerMaxHeight,
	})
}

func (p *twoPhasePicker) ShortHelp() []key.Binding {
	return []key.Binding{
		p.keyMap.UpDown,
		p.keyMap.Select,
		p.keyMap.Back,
		p.keyMap.Close,
	}
}

func (p *twoPhasePicker) FullHelp() [][]key.Binding {
	return [][]key.Binding{p.ShortHelp()}
}
