package dialog

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/completions"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
)

// MentionPickerID is the identifier for the @-mention picker dialog.
const MentionPickerID = "mention"

// mentionPickerMaxHeight caps the mention picker.
const mentionPickerMaxHeight = 20

// ActionMentionSelected is emitted when the user picks a mention.
// Value is one of the completions value types (a file path, an MCP
// resource, or a subagent name); the UI model owns what insertion each
// kind performs.
type ActionMentionSelected struct {
	Value any
}

// ActionMentionCancelled is emitted when the picker closes without a
// selection; the model removes the "@" that opened it from the editor.
type ActionMentionCancelled struct{}

// MentionPicker is the @-mention picker: the same filter-input dialog
// surface every picker uses, fed by the completions package's sources
// and ranked by its name-priority filter.
type MentionPicker struct {
	com     *common.Common
	list    *list.FilterableList
	input   textinput.Model
	loading bool
	// items holds the unfiltered mentions; the name-priority filter
	// re-ranks from it on every keystroke.
	items []list.FilterableItem

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*MentionPicker)(nil)

// NewMentionPicker creates the mention picker and the command that
// loads its sources: files and MCP resources off-thread, subagents
// from memory.
func NewMentionPicker(com *common.Common, subagents []completions.SubagentCompletionValue, depth, limit int) (*MentionPicker, tea.Cmd) {
	p := &MentionPicker{com: com, loading: true}

	p.list = list.NewFilterableList()
	p.list.Focus()

	p.input = textinput.New()
	p.input.SetVirtualCursor(false)
	p.input.Prompt = "❯ "
	p.input.Placeholder = "Search files, agents and resources"
	p.input.SetStyles(com.Styles.TextInput)
	p.input.Focus()

	km := dialogKeys()
	p.keyMap.Select = keys.WithDesc(km.Select, "mention")
	p.keyMap.Next = km.Next
	p.keyMap.Previous = km.Previous
	p.keyMap.UpDown = km.UpDown
	p.keyMap.Close = km.Close

	return p, completions.LoadItems(depth, limit, subagents)
}

// ID implements Dialog.
func (p *MentionPicker) ID() string {
	return MentionPickerID
}

// HandleMsg implements Dialog.
func (p *MentionPicker) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case completions.CompletionItemsLoadedMsg:
		t := p.com.Styles
		// Rows use the shared dialog item tokens: transparent normal
		// rows, selection background only on the focused row.
		p.items = completions.MentionItems(
			t.Dialog.NormalItem, t.Dialog.SelectedItem, t.Completions.Match,
			msg.Files, msg.Resources, msg.Subagents,
		)
		p.list.SetItems(p.items...)
		p.list.SelectFirst()
		p.list.ScrollToSelected()
		p.loading = false
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, p.keyMap.Close):
			return ActionMentionCancelled{}
		case key.Matches(msg, p.keyMap.Previous):
			if p.list.IsSelectedFirst() {
				p.list.SelectLast()
			} else {
				p.list.SelectPrev()
			}
			p.list.ScrollToSelected()
		case key.Matches(msg, p.keyMap.Next):
			if p.list.IsSelectedLast() {
				p.list.SelectFirst()
			} else {
				p.list.SelectNext()
			}
			p.list.ScrollToSelected()
		case key.Matches(msg, p.keyMap.Select):
			if item, ok := p.list.SelectedItem().(*completions.CompletionItem); ok && item != nil {
				return ActionMentionSelected{Value: item.Value()}
			}
		default:
			cmd, value, changed := updateFilterInput(&p.input, msg)
			if changed {
				// Mentions keep the tiered name-priority ranking on
				// top of the plain fuzzy filter.
				completions.FilterMentionItems(p.list, p.items, value)
			}
			return ActionCmd{cmd}
		}
	}
	return nil
}

// Draw implements Dialog.
func (p *MentionPicker) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	st := p.com.Styles
	width := DialogWidth(st, area)
	height := DialogHeightCeiling(st, area, mentionPickerMaxHeight)
	innerWidth := DialogInnerWidth(st, width)

	p.input.SetWidth(dialogInputTextWidth(st, p.input, innerWidth))

	listHeight, listTotalHeight, _ := sizeDialogList(st, p.list, innerWidth, height, true)

	rc := NewRenderContext(st, width)
	rc.Title = "Mention"
	rc.AddInput(p.input.View())

	if p.loading {
		rc.AddPart(st.Dialog.SecondaryText.Render("Loading mentions..."))
	} else if p.list.Len() == 0 {
		rc.AddPart(st.Dialog.SecondaryText.Render("No matches."))
	} else {
		p.list.ScrollToSelected()
		listView := st.Dialog.List.Height(p.list.Height()).Render(p.list.Render())
		listView = joinScrollbar(st, listView, listHeight, listTotalHeight, listHeight, p.list.Offset())
		rc.AddPart(listView)
	}

	view := rc.Render()
	cur := DialogCursor(st, view, p.input.Cursor())
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements help.KeyMap.
func (p *MentionPicker) ShortHelp() []key.Binding {
	return []key.Binding{
		p.keyMap.UpDown,
		p.keyMap.Select,
		p.keyMap.Close,
	}
}

// FullHelp implements help.KeyMap.
func (p *MentionPicker) FullHelp() [][]key.Binding {
	return [][]key.Binding{p.ShortHelp()}
}
