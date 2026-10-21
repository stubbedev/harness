package dialog

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// CommandsID is the identifier for the commands dialog.
const CommandsID ID = "commands"

// CommandType represents the type of commands being displayed.
type CommandType uint

// String returns the string representation of the CommandType.
func (c CommandType) String() string {
	return []string{"System", "User", "MCP", "Skills"}[c]
}

const (
	SystemCommands CommandType = iota
	UserCommands
	MCPPrompts
	SkillsCommands
)

// Commands represents a dialog that shows available commands.
type Commands struct {
	com    *common.Common
	keyMap struct {
		Select,
		UpDown,
		Next,
		Previous,
		Tab,
		ShiftTab,
		Close key.Binding
	}

	catalog   *Catalog
	selected  CommandType
	searching bool

	input textinput.Model
	list  *list.FilterableList
}

var _ Dialog = (*Commands)(nil)

// NewCommands creates the command palette listing catalog.
func NewCommands(com *common.Common, catalog *Catalog) (*Commands, error) {
	c := &Commands{
		com:      com,
		selected: SystemCommands,
		catalog:  catalog,
	}

	c.list = list.NewFilterableList()
	c.list.SetFilterOrder(commandOrder)
	c.list.Focus()
	c.list.SetSelected(0)

	c.input = textinput.New()
	c.input.SetVirtualCursor(false)
	c.input.Prompt = "❯ "
	c.input.Placeholder = "Type to filter"
	c.input.SetStyles(com.Styles.TextInput)
	c.input.Focus()

	km := dialogKeys()
	c.keyMap.Select = km.Select
	c.keyMap.UpDown = km.UpDown
	c.keyMap.Next = km.Next
	c.keyMap.Previous = km.Previous
	c.keyMap.Tab = km.Commands.Tab
	c.keyMap.ShiftTab = km.Commands.ShiftTab
	c.keyMap.Close = keys.WithDesc(km.Close, "cancel")

	c.setCommandItems(c.selected)

	return c, nil
}

// ID implements Dialog.
func (c *Commands) ID() ID {
	return CommandsID
}

// HandleMsg implements [Dialog].
func (c *Commands) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, c.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, c.keyMap.Previous):
			c.list.Focus()
			selectPrevWrap(c.list)
		case key.Matches(msg, c.keyMap.Next):
			c.list.Focus()
			selectNextWrap(c.list)
		case key.Matches(msg, c.keyMap.Select):
			if selectedItem := c.list.SelectedItem(); selectedItem != nil {
				if item, ok := selectedItem.(*CommandItem); ok && item != nil {
					return item.SelectAction()
				}
			}
		case key.Matches(msg, c.keyMap.Tab):
			c.cycleTab(1)
		case key.Matches(msg, c.keyMap.ShiftTab):
			c.cycleTab(-1)
		default:
			for _, item := range c.list.FilteredItems() {
				if item, ok := item.(*CommandItem); ok && item != nil {
					if key.Matches(msg, item.Shortcut()) {
						return item.SelectAction()
					}
				}
			}
			cmd, _ := filterInput(&c.input, msg, c.applyFilter)
			return ActionCmd{cmd}
		}
	}
	return nil
}

func (c *Commands) InitialCmd() tea.Cmd {
	return nil
}

// commandsRadioView generates the command type selector radio buttons.
func commandsRadioView(sty *styles.Styles, tabs []commandMenu, selected CommandType) string {
	if len(tabs) < 2 {
		return ""
	}

	selectedFn := func(t CommandType) string {
		if t == selected {
			return sty.Radio.On.Padding(0, 1).Render() + sty.Radio.Label.Render(t.String())
		}
		return sty.Radio.Off.Padding(0, 1).Render() + sty.Radio.Label.Render(t.String())
	}

	parts := make([]string, 0, len(tabs))
	for _, m := range tabs {
		parts = append(parts, selectedFn(m.tab()))
	}
	return strings.Join(parts, " ")
}

// Draw implements [Dialog].
func (c *Commands) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := c.com.Styles
	applyInfoColumnVisibility(c.list.FilteredItems(), DialogInnerWidth(t, DialogWidth(t, area)), commandInfoMaxPercent)

	return drawPicker(scr, area, t, &c.input, c.list, pickerLayout{
		title:     "Commands",
		titleInfo: commandsRadioView(t, c.tabs(), c.selected),
		maxHeight: defaultDialogHeight,
	})
}

// ShortHelp implements [help.KeyMap].
func (c *Commands) ShortHelp() []key.Binding {
	binds := []key.Binding{c.keyMap.UpDown, c.keyMap.Select}
	if c.hasTabs() {
		binds = append([]key.Binding{c.keyMap.Tab}, binds...)
	}
	return append(binds, c.keyMap.Close)
}

// FullHelp implements [help.KeyMap].
func (c *Commands) FullHelp() [][]key.Binding {
	row := []key.Binding{c.keyMap.Select, c.keyMap.Next, c.keyMap.Previous}
	if c.hasTabs() {
		row = append(row, c.keyMap.Tab)
	}
	return [][]key.Binding{row, {c.keyMap.Close}}
}

// hasTabs reports whether the tab switcher has anywhere to go: two or
// more menus with items. The Tab bindings are hinted only when this
// holds, so the help never advertises a dead key.
func (c *Commands) hasTabs() bool {
	return len(c.tabs()) > 1
}

// cycleTab moves the selection to the next menu tab with items (step -1
// for the previous one), wrapping. No-op when there is nothing to
// switch to.
func (c *Commands) cycleTab(step int) {
	tabs := c.tabs()
	if len(tabs) < 2 {
		return
	}
	for i, m := range tabs {
		if m.tab() == c.selected {
			c.setCommandItems(tabs[(i+step+len(tabs))%len(tabs)].tab())
			return
		}
	}
	// The selected tab lost its content; land on the first one.
	c.setCommandItems(tabs[0].tab())
}

// tabs returns the menus that currently have items: the pages the tab
// switcher can land on.
func (c *Commands) tabs() []commandMenu {
	var tabs []commandMenu
	for _, m := range c.catalog.menus() {
		if m.hasItems() {
			tabs = append(tabs, m)
		}
	}
	return tabs
}

// ownKeys returns the bindings the palette handles before any command
// shortcut. Their keys are stripped from every shortcut, so an entry is
// never hinted with, nor expected to fire on, a key the palette consumes.
func (c *Commands) ownKeys() []key.Binding {
	return []key.Binding{
		c.keyMap.Close,
		c.keyMap.Previous,
		c.keyMap.Next,
		c.keyMap.Select,
		c.keyMap.Tab,
		c.keyMap.ShiftTab,
	}
}

// setCommandItems selects the tab the empty query lists. A query in the
// input keeps searching every tab.
func (c *Commands) setCommandItems(commandType CommandType) {
	c.selected = commandType
	if c.searching {
		return
	}
	c.loadItems()
	c.list.ScrollToTop()
	c.list.SetSelected(0)
}

func (c *Commands) applyFilter(query string) {
	if (query != "") != c.searching {
		c.searching = query != ""
		c.loadItems()
	}
	applyListFilter(c.list)(query)
}

func (c *Commands) loadItems() {
	tag := c.searching && c.hasTabs()
	var commandItems []list.FilterableItem
	for _, m := range c.catalog.menus() {
		if !c.searching && m.tab() != c.selected {
			continue
		}
		for _, item := range m.items() {
			item = item.WithShortcut(keys.Without(item.Shortcut(), c.ownKeys()...))
			if tag {
				item = item.WithTag(m.tab().String())
			}
			commandItems = append(commandItems, item)
		}
	}
	c.list.SetItems(commandItems...)
}

// SetCustomCommands sets the custom commands and refreshes the listed
// items.
func (c *Commands) SetCustomCommands(customCommands []commands.CustomCommand) {
	c.catalog.customCommands = customCommands
	c.loadItems()
}

// SetMCPPrompts sets the MCP prompts and refreshes the listed items.
func (c *Commands) SetMCPPrompts(mcpPrompts []commands.MCPPrompt) {
	c.catalog.mcpPrompts = mcpPrompts
	c.loadItems()
}
