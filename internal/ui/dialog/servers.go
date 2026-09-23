package dialog

import (
	"fmt"
	"slices"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
)

// This file holds the machinery shared by the MCP and LSP server
// manager dialogs: a two-phase picker — the server list, then one
// server's facts and actions — over a live source. A manager for
// another kind of server is a serversSource implementation plus the
// palette plumbing; nothing here learns the kind.

// serversDialogMaxHeight caps the server manager dialogs.
const serversDialogMaxHeight = 24

type serversPhase uint8

const (
	serversPhaseList serversPhase = iota
	serversPhaseDetail
)

// serversSource supplies everything kind-specific about one server
// manager dialog.
type serversSource interface {
	// dialogID identifies the dialog in the overlay.
	dialogID() ID
	// title is the dialog title in the list phase.
	title() string
	// detailTitle is the dialog title in the detail phase.
	detailTitle(name string) string
	// emptyText is shown when there are no servers.
	emptyText() string
	// entries lists the server names in display order.
	entries() []string
	// statusText is the one-line status under a server name.
	statusText(name string) string
	// detailItems renders the fact rows and action rows for a server.
	detailItems(name string) []list.FilterableItem
}

// newServersDialog builds the shared state machine for src. The
// concrete dialog embeds the result; selectKey and backKey come from
// the kind's own rebindable group, the rest from the shared dialog set.
// The returned value must be embedded before the first refresh: src's
// methods read state through the embedding struct, which is still the
// zero value until the assignment completes. Callers therefore call
// [serversDialog.refresh] themselves after embedding.
func newServersDialog(com *common.Common, src serversSource, selectKey, backKey key.Binding) serversDialog {
	km := dialogKeys()
	d := serversDialog{
		com:    com,
		source: src,
		phase:  serversPhaseList,
	}
	d.keyMap.Select = selectKey
	d.keyMap.Back = backKey
	d.keyMap.Next = keys.WithDesc(km.Next, "next")
	d.keyMap.Previous = keys.WithDesc(km.Previous, "previous")
	d.keyMap.UpDown = km.UpDown
	d.keyMap.Close = keys.WithDesc(km.Close, "close")

	d.list = list.NewFilterableList()
	d.list.Focus()

	d.input = textinput.New()
	d.input.SetVirtualCursor(false)
	d.input.Prompt = "❯ "
	d.input.Placeholder = "Filter servers"
	d.input.SetStyles(com.Styles.TextInput)
	d.input.Focus()
	return d
}

// serversDialog is the shared state machine of a server manager. The
// concrete dialog embeds it and implements [serversSource].
type serversDialog struct {
	com    *common.Common
	source serversSource
	list   *list.FilterableList
	input  textinput.Model
	phase  serversPhase
	// current is the server the detail phase shows.
	current string
	// listNames parallels the list-phase items, so a refresh can keep
	// the selection on the same server after the entries reorder.
	listNames []string

	keyMap struct {
		Select   key.Binding
		Back     key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

// ID implements Dialog.
func (d *serversDialog) ID() ID {
	return d.source.dialogID()
}

// HandleMsg implements [Dialog].
func (d *serversDialog) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, d.keyMap.Close):
			if d.phase == serversPhaseDetail {
				d.enterListPhase()
				return nil
			}
			return ActionClose{}
		case key.Matches(msg, d.keyMap.Back) && d.phase == serversPhaseDetail:
			d.enterListPhase()
			return nil
		case key.Matches(msg, d.keyMap.Previous):
			selectPrevWrap(d.list)
		case key.Matches(msg, d.keyMap.Next):
			selectNextWrap(d.list)
		case key.Matches(msg, d.keyMap.Select):
			return d.confirmSelection()
		default:
			cmd, _ := filterInput(&d.input, msg, applyListFilter(d.list))
			return ActionCmd{cmd}
		}
	}
	return nil
}

// confirmSelection opens the selected server's detail view, or runs
// the selected action row within one. The selected server resolves
// through the selected item's title, not its position: filtering
// reorders the list under the selection.
func (d *serversDialog) confirmSelection() Action {
	if d.phase == serversPhaseList {
		if ci, ok := d.list.SelectedItem().(*CommandItem); ok && ci != nil && slices.Contains(d.listNames, ci.Title()) {
			d.current = ci.Title()
			d.phase = serversPhaseDetail
			d.refreshDetail()
		}
		return nil
	}
	item := d.list.SelectedItem()
	if item == nil {
		return nil
	}
	if ci, ok := item.(*CommandItem); ok && ci != nil {
		if action := ci.Action(); action != nil {
			return action
		}
	}
	return nil
}

// enterListPhase returns from a detail view to the server list.
func (d *serversDialog) enterListPhase() {
	d.phase = serversPhaseList
	d.refreshServers()
}

// refresh rebuilds whichever view is showing, for when fresh live
// state arrived.
func (d *serversDialog) refresh() {
	if d.phase == serversPhaseDetail {
		d.refreshDetail()
		return
	}
	d.refreshServers()
}

// refreshServers rebuilds the server list, keeping the selection on
// the same server when the entries reorder.
func (d *serversDialog) refreshServers() {
	keep := ""
	if ci, ok := d.list.SelectedItem().(*CommandItem); ok && ci != nil {
		keep = ci.Title()
	}

	d.listNames = d.source.entries()
	items := make([]list.FilterableItem, len(d.listNames))
	for i, name := range d.listNames {
		items[i] = NewCommandItem(d.com.Styles, fmt.Sprintf("server_%d", i), name, "", nil).
			WithDescription(d.source.statusText(name))
	}
	d.list.SetItems(items...)

	selected := 0
	if keep != "" && slices.Contains(d.listNames, keep) {
		selected = slices.Index(d.listNames, keep)
	} else if idx := d.list.Selected(); idx >= 0 && len(items) > 0 {
		selected = min(idx, len(items)-1)
	}
	if len(items) > 0 {
		d.list.SetSelected(selected)
	}
}

// refreshDetail rebuilds the detail view of the current server.
func (d *serversDialog) refreshDetail() {
	d.list.SetItems(d.source.detailItems(d.current)...)
	d.list.SetSelected(0)
	d.list.ScrollToTop()
}

// Draw implements [Dialog].
func (d *serversDialog) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	st := d.com.Styles
	width := DialogWidth(st, area)
	innerWidth := DialogInnerWidth(st, width)

	listHeight, listTotalHeight, _ := sizeDialogList(st, d.list, innerWidth, serversDialogMaxHeight, true)

	rc := NewRenderContext(st, width)
	if d.phase == serversPhaseList {
		rc.Title = d.source.title()
		if len(d.listNames) == 0 {
			rc.AddPart(st.Dialog.NormalItem.Render(d.source.emptyText()))
		}
	} else {
		rc.Title = d.source.detailTitle(d.current)
	}

	d.list.ScrollToSelected()
	listView := st.Dialog.List.Height(d.list.Height()).Render(d.list.Render())
	listView = joinScrollbar(st, listView, listHeight, listTotalHeight, listHeight, d.list.Offset())
	rc.AddPart(listView)
	rc.AddInput(d.input.View())

	view := rc.Render()
	cur := DialogCursor(st, view, d.input.Cursor())
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements help.KeyMap.
func (d *serversDialog) ShortHelp() []key.Binding {
	return []key.Binding{
		d.keyMap.UpDown,
		d.keyMap.Select,
		d.keyMap.Back,
		d.keyMap.Close,
	}
}

// FullHelp implements help.KeyMap.
func (d *serversDialog) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}

// detailBuilder assembles one server's detail view: fact rows
// followed by action rows, with unique item IDs.
type detailBuilder struct {
	com    *common.Common
	prefix string
	items  []list.FilterableItem
}

// newDetailBuilder starts a detail view whose item IDs are prefixed by
// the kind, e.g. "mcp".
func newDetailBuilder(com *common.Common, prefix string) *detailBuilder {
	return &detailBuilder{com: com, prefix: prefix}
}

// Row appends a fact row; selecting it does nothing.
func (b *detailBuilder) Row(text string) *detailBuilder {
	b.items = append(b.items, NewCommandItem(
		b.com.Styles,
		fmt.Sprintf("%s_row_%d", b.prefix, len(b.items)),
		text,
		"",
		nil,
	))
	return b
}

// Action appends an action row; selecting it returns act.
func (b *detailBuilder) Action(id, title, desc string, act Action) *detailBuilder {
	b.items = append(b.items, NewCommandItem(b.com.Styles, b.prefix+"_"+id, title, "", act).WithDescription(desc))
	return b
}

// Items returns the assembled rows.
func (b *detailBuilder) Items() []list.FilterableItem {
	return b.items
}
