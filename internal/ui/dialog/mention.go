package dialog

import (
	"cmp"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/completions"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// MentionPickerID is the identifier for the @-mention picker dialog.
const MentionPickerID ID = "mention"

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
func (p *MentionPicker) ID() ID {
	return MentionPickerID
}

// HandleMsg implements Dialog.
func (p *MentionPicker) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case completions.CompletionItemsLoadedMsg:
		p.items = mentionPickerItems(p.com.Styles, msg)
		p.list.SetItems(p.items...)
		p.list.SelectFirst()
		p.list.ScrollToSelected()
		p.loading = false
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, p.keyMap.Close):
			return ActionMentionCancelled{}
		case key.Matches(msg, p.keyMap.Previous):
			selectPrevWrap(p.list)
		case key.Matches(msg, p.keyMap.Next):
			selectNextWrap(p.list)
		case key.Matches(msg, p.keyMap.Select):
			if item, ok := p.list.SelectedItem().(PickerItem); ok && item != nil {
				return ActionMentionSelected{Value: item.Value()}
			}
		default:
			// The typing path flows through the shared input helper; only
			// the search differs - mentions rank by name priority.
			cmd, _ := filterInput(&p.input, msg, func(query string) {
				completions.FilterMentionItems(p.list, p.items, query)
			})
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

// mentionPickerItems builds the mention rows through the shared
// picker item: subagents first (they sit at the top), then files, then
// MCP resources, each labeled with its kind on the right.
func mentionPickerItems(t *styles.Styles, msg completions.CompletionItemsLoadedMsg) []list.FilterableItem {
	items := make([]list.FilterableItem, 0, len(msg.Subagents)+len(msg.Files)+len(msg.Resources))
	for _, sa := range msg.Subagents {
		items = append(items, NewPickerItem(t, sa, sa.Name, "agent"))
	}
	for _, file := range msg.Files {
		items = append(items, NewPickerItem(t, file, file.Path, "file"))
	}
	for _, res := range msg.Resources {
		label := res.MCPName + "/" + cmp.Or(res.Title, res.URI)
		items = append(items, NewPickerItem(t, res, label, "resource"))
	}
	return items
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
