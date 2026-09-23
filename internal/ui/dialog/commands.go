package dialog

import (
	"os"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/config"
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

	sessionID  string
	hasSession bool
	hasSummary bool
	hasTodos   bool
	selected   CommandType

	spinner spinner.Model
	loading bool

	input textinput.Model
	list  *list.FilterableList

	windowWidth int

	customCommands []commands.CustomCommand
	mcpPrompts     []commands.MCPPrompt

	// skillsOnly restricts the dialog to the skills palette: a single
	// list of agent skills with no tab cycling.
	skillsOnly bool
}

var _ Dialog = (*Commands)(nil)

// NewCommands creates a new commands dialog.
func NewCommands(com *common.Common, sessionID string, hasSession, hasSummary, hasTodos bool, customCommands []commands.CustomCommand, mcpPrompts []commands.MCPPrompt) (*Commands, error) {
	c := &Commands{
		com:            com,
		selected:       SystemCommands,
		sessionID:      sessionID,
		hasSession:     hasSession,
		hasSummary:     hasSummary,
		hasTodos:       hasTodos,
		customCommands: customCommands,
		mcpPrompts:     mcpPrompts,
	}

	c.list = list.NewFilterableList()
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

	// Set initial commands
	c.setCommandItems(c.selected)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = com.Styles.Dialog.Spinner
	c.spinner = s

	return c, nil
}

// ID implements Dialog.
func (c *Commands) ID() ID {
	return CommandsID
}

// NewSkills creates a dialog listing only agent skills, the palette
// behind the "/" prefix.
func NewSkills(com *common.Common, customCommands []commands.CustomCommand) (*Commands, error) {
	c, err := NewCommands(com, "", false, false, false, customCommands, nil)
	if err != nil {
		return nil, err
	}
	c.skillsOnly = true
	c.setCommandItems(SkillsCommands)
	return c, nil
}

// SkillsOnly reports whether the dialog is the skills palette.
func (c *Commands) SkillsOnly() bool {
	return c.skillsOnly
}

// HandleMsg implements [Dialog].
func (c *Commands) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if c.loading {
			var cmd tea.Cmd
			c.spinner, cmd = c.spinner.Update(msg)
			return ActionCmd{Cmd: cmd}
		}
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, c.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, c.keyMap.Previous):
			c.list.Focus()
			if c.list.IsSelectedFirst() {
				c.list.SelectLast()
			} else {
				c.list.SelectPrev()
			}
			c.list.ScrollToSelected()
		case key.Matches(msg, c.keyMap.Next):
			c.list.Focus()
			if c.list.IsSelectedLast() {
				c.list.SelectFirst()
			} else {
				c.list.SelectNext()
			}
			c.list.ScrollToSelected()
		case key.Matches(msg, c.keyMap.Select):
			if selectedItem := c.list.SelectedItem(); selectedItem != nil {
				if item, ok := selectedItem.(*CommandItem); ok && item != nil {
					return item.Action()
				}
			}
		case key.Matches(msg, c.keyMap.Tab):
			c.cycleTab(1)
		case key.Matches(msg, c.keyMap.ShiftTab):
			c.cycleTab(-1)
		default:
			var cmd tea.Cmd
			for _, item := range c.list.FilteredItems() {
				if item, ok := item.(*CommandItem); ok && item != nil {
					if msg.String() == item.Shortcut() {
						return item.Action()
					}
				}
			}
			cmd, _ = filterInput(&c.input, msg, applyListFilter(c.list))
			return ActionCmd{cmd}
		}
	}
	return nil
}

func (c *Commands) InitialCmd() tea.Cmd {
	return nil
}

// Cursor returns the cursor position relative to the dialog.
func (c *Commands) Cursor() *tea.Cursor {
	return InputCursor(c.com.Styles, c.input.Cursor())
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
	width := DialogWidth(t, area)
	height := DialogHeightCeiling(t, area, defaultDialogHeight)
	if area.Dx() != c.windowWidth && c.selected == SystemCommands {
		c.windowWidth = area.Dx()
		// since some items in the list depend on width (e.g. toggle sidebar command),
		// we need to reset the command items when width changes
		c.setCommandItems(c.selected)
	}

	innerWidth := DialogInnerWidth(t, width)
	heightOffset := dialogChromeHeight(t, t.Dialog.HelpView)
	// Hug the content: the list viewport never exceeds its item count, so
	// a short list shrinks the panel instead of padding blank rows.
	listHeight := min(max(0, height-heightOffset), c.list.TotalHeight())

	c.input.SetWidth(dialogInputTextWidth(t, c.input, innerWidth))

	c.list.SetSize(innerWidth, listHeight)

	// Hide the shortcut hints uniformly when the widest would crowd names.
	applyInfoColumnVisibility(c.list.FilteredItems(), innerWidth, commandInfoMaxPercent)

	rc := NewRenderContext(t, width)
	rc.Title = "Commands"
	tabs := c.tabs()
	if c.skillsOnly {
		rc.Title = "Skills"
	} else {
		rc.TitleInfo = commandsRadioView(t, tabs, c.selected)
	}
	rc.AddInput(c.input.View())
	listView := t.Dialog.List.Height(c.list.Height()).Render(c.list.Render())
	rc.AddPart(listView)

	if c.loading {
		rc.AddPart(t.Dialog.HelpView.Width(innerWidth).Render(c.spinner.View() + " Generating Prompt..."))
	}

	view := rc.Render()

	cur := DialogCursor(t, view, c.input.Cursor())
	DrawCenterCursor(scr, area, view, cur)
	return cur
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
			c.selected = tabs[(i+step+len(tabs))%len(tabs)].tab()
			c.setCommandItems(c.selected)
			return
		}
	}
	// The selected tab lost its content; land on the first one.
	c.setCommandItems(tabs[0].tab())
}

// commandMenu is one content source of the commands palette: a page of
// items the tab switcher can land on. Tab presence, the tab hint, the
// radio row and tab cycling are all derived from the menus registered
// in menus(), so a future source becomes a tab by implementing this
// interface and joining that list; nothing else in the dialog learns
// its name.
type commandMenu interface {
	// tab is the palette page this menu fills.
	tab() CommandType
	// hasItems reports whether the menu currently contributes a tab.
	// An empty menu is not a tab: nothing to switch to, nothing to hint.
	hasItems() bool
	// items renders the menu's entries.
	items() []*CommandItem
}

// systemMenu is the built-in commands page. Always present.
type systemMenu struct{ c *Commands }

func (systemMenu) tab() CommandType        { return SystemCommands }
func (systemMenu) hasItems() bool          { return true }
func (m systemMenu) items() []*CommandItem { return m.c.defaultCommands() }

// userMenu is the user-defined command page: custom and extension
// commands. Skills are excluded; they have their own palette.
type userMenu struct{ c *Commands }

func (userMenu) tab() CommandType { return UserCommands }
func (m userMenu) hasItems() bool { return len(m.c.userCommands()) > 0 }
func (m userMenu) items() []*CommandItem {
	var items []*CommandItem
	for _, cmd := range m.c.userCommands() {
		action := ActionRunCustomCommand{
			Content:     cmd.Content,
			Arguments:   cmd.Arguments,
			Skill:       cmd.Skill,
			ExtensionID: cmd.ExtensionID,
		}
		item := NewCommandItem(m.c.com.Styles, "custom_"+cmd.ID, cmd.Name, "", action)
		if cmd.Description != "" {
			item = item.WithDescription(cmd.Description)
		}
		items = append(items, item)
	}
	return items
}

// mcpMenu is the MCP prompt page.
type mcpMenu struct{ c *Commands }

func (mcpMenu) tab() CommandType { return MCPPrompts }
func (m mcpMenu) hasItems() bool { return len(m.c.mcpPrompts) > 0 }
func (m mcpMenu) items() []*CommandItem {
	var items []*CommandItem
	for _, cmd := range m.c.mcpPrompts {
		action := ActionRunMCPPrompt{
			Title:       cmd.Title,
			Description: cmd.Description,
			PromptID:    cmd.PromptID,
			ClientID:    cmd.ClientID,
			Arguments:   cmd.Arguments,
		}
		items = append(items, NewCommandItem(m.c.com.Styles, "mcp_"+cmd.ID, cmd.PromptID, "", action))
	}
	return items
}

// skillsMenu is the skills palette page behind "/".
type skillsMenu struct{ c *Commands }

func (skillsMenu) tab() CommandType { return SkillsCommands }
func (m skillsMenu) hasItems() bool { return len(m.items()) > 0 }
func (m skillsMenu) items() []*CommandItem {
	var items []*CommandItem
	for _, cmd := range m.c.customCommands {
		if cmd.Skill == nil {
			continue
		}
		action := ActionAttachSkill{ID: cmd.Skill.SkillFilePath, Name: cmd.Skill.Name}
		item := NewCommandItem(m.c.com.Styles, "custom_"+cmd.ID, cmd.Name, "", action)
		item = item.WithDescription(cmd.Skill.Description)
		// The source prefix (project:/user:/system:) labels where the
		// skill lives, not its name; match on the name after it.
		if _, name, ok := strings.Cut(cmd.Name, ":"); ok {
			item = item.WithFilterTitle(name)
		}
		items = append(items, item)
	}
	return items
}

// menus returns the palette's content sources in tab order. The skills
// palette replaces the whole set: it lists skills and nothing else.
func (c *Commands) menus() []commandMenu {
	if c.skillsOnly {
		return []commandMenu{skillsMenu{c}}
	}
	return []commandMenu{systemMenu{c}, userMenu{c}, mcpMenu{c}}
}

// tabs returns the menus that currently have items: the pages the tab
// switcher can land on.
func (c *Commands) tabs() []commandMenu {
	var tabs []commandMenu
	for _, m := range c.menus() {
		if m.hasItems() {
			tabs = append(tabs, m)
		}
	}
	return tabs
}

// userCommands returns the custom commands that are not skills.
func (c *Commands) userCommands() []commands.CustomCommand {
	var out []commands.CustomCommand
	for _, cmd := range c.customCommands {
		if cmd.Skill == nil {
			out = append(out, cmd)
		}
	}
	return out
}

// setCommandItems sets the command items based on the specified command type.
func (c *Commands) setCommandItems(commandType CommandType) {
	c.selected = commandType

	var commandItems []list.FilterableItem
	for _, m := range c.menus() {
		if m.tab() == commandType {
			for _, item := range m.items() {
				commandItems = append(commandItems, item)
			}
			break
		}
	}

	c.list.SetItems(commandItems...)
	c.list.SetFilter("")
	c.list.ScrollToTop()
	c.list.SetSelected(0)
	c.input.SetValue("")
}

// compactArguments defines the optional /compact focus prompt. Declared at
// package scope because the local variable in defaultCommands shadows the
// commands package.
var compactArguments = []commands.Argument{{
	ID:          "instructions",
	Title:       "Focus",
	Description: "Optional: what the compacted summary should keep. Leave empty for a general summary.",
}}

// defaultCommands returns the list of default system commands.
// defaultCommands returns the list of default system commands. Shortcut
// labels follow the keymap: a rebound action shows its new key here, and
// the palette matches the label literally, so the two never drift apart.
func (c *Commands) defaultCommands() []*CommandItem {
	km := c.com.KeyMap()
	commands := []*CommandItem{
		NewCommandItem(c.com.Styles, "new_session", "New Session", km.Chat.NewSession.Help().Key, ActionNewSession{}).WithAliases("clear"),
		NewCommandItem(c.com.Styles, "switch_session", "Sessions", km.Sessions.Help().Key, ActionOpenDialog{SessionsID}).WithAliases("resume", "switch"),
		NewCommandItem(c.com.Styles, "switch_model", "Switch Model", km.Models.Help().Key, ActionOpenDialog{ModelsID}),
		NewCommandItem(c.com.Styles, "connect_provider", "Connect Provider", "", ActionOpenDialog{ConnectID}).WithAliases("provider", "auth", "login"),
		NewCommandItem(c.com.Styles, "switch_theme", "Switch Theme", km.Themes.Help().Key, ActionOpenDialog{ThemesID}),
		NewCommandItem(c.com.Styles, "mcp_servers", "MCP Servers", "", ActionOpenDialog{DialogID: MCPServersID}).WithAliases("mcp"),
		NewCommandItem(c.com.Styles, "lsp_servers", "LSP Servers", "", ActionOpenDialog{DialogID: LSPServersID}).WithAliases("lsp"),
	}

	// Only show compact command if there's an active session
	if c.hasSession {
		commands = append(commands, NewCommandItem(c.com.Styles, "summarize", "Summarize Session", "", ActionSummarize{SessionID: c.sessionID}))
		commands = append(commands, NewCommandItem(c.com.Styles, "rewind", "Rewind to Earlier Turn", "", ActionOpenDialog{RewindID}))
		commands = append(commands, NewCommandItem(c.com.Styles, "compact", "Compact Session (with focus)", "", ActionCompact{
			SessionID: c.sessionID,
			Arguments: compactArguments,
		}))
	}

	// Only show the export command when there is a conversation to export
	if c.hasSession {
		commands = append(commands, NewCommandItem(c.com.Styles, "export_conversation", "Export Conversation", km.ExportConversation.Help().Key, ActionExportConversation{SessionID: c.sessionID}).WithAliases("export", "transcript", "copy"))
	}

	// Only show the save summary command if the session already has one
	if c.hasSession && c.hasSummary {
		commands = append(commands, NewCommandItem(c.com.Styles, "save_summary", "Save Session Summary", "", ActionSaveSummary{SessionID: c.sessionID}))
	}

	// Add reasoning toggle for models that support it
	cfg := c.com.Config()
	if agentCfg, ok := cfg.Agents[config.AgentCoder]; ok {
		providerCfg := cfg.GetProviderForModel(agentCfg.Model)
		model := cfg.GetModelByType(agentCfg.Model)
		if providerCfg != nil && model != nil && model.CanReason {
			selectedModel := cfg.Models[agentCfg.Model]

			// Anthropic models: thinking toggle
			if model.CanReason && len(model.ReasoningLevels) == 0 {
				status := "Enable"
				if selectedModel.Think {
					status = "Disable"
				}
				commands = append(commands, NewCommandItem(c.com.Styles, "toggle_thinking", status+" Thinking Mode", "", ActionToggleThinking{}))
			}

			// OpenAI models: reasoning effort dialog
			if len(model.ReasoningLevels) > 0 {
				commands = append(commands, NewCommandItem(c.com.Styles, "select_reasoning_effort", "Select Reasoning Effort", "", ActionOpenDialog{
					DialogID: ReasoningID,
				}))
			}
		}
	}
	// Add external editor command if $EDITOR is available.
	//
	// TODO: Use [tea.EnvMsg] to get environment variable instead of os.Getenv;
	// because os.Getenv does IO is breaks the TEA paradigm and is generally an
	// antipattern.
	if os.Getenv("EDITOR") != "" {
		commands = append(commands, NewCommandItem(c.com.Styles, "open_external_editor", "Open External Editor", km.Editor.OpenEditor.Help().Key, ActionExternalEditor{}))
	}

	if c.hasTodos {
		commands = append(commands, NewCommandItem(c.com.Styles, "toggle_pills", "Toggle To-Dos", km.Chat.TogglePills.Help().Key, ActionTogglePills{}))
	}

	// Add a command for selecting notification style via picker dialog.
	notificationLabel := "Notification Style"
	commands = append(commands, NewCommandItem(c.com.Styles, "select_notifications", notificationLabel, "", ActionOpenDialog{DialogID: NotificationsID}))

	commands = append(
		commands,
		NewCommandItem(c.com.Styles, "toggle_help", "Toggle Help", km.Help.Help().Key, ActionToggleHelp{}),
		NewCommandItem(c.com.Styles, "init", "Initialize Project", "", ActionInitializeProject{}),
	)

	// Add mouse support toggle.
	mouseLabel := "Disable Mouse"
	if cfg != nil && cfg.Options != nil && cfg.Options.TUI.Mouse != nil && !*cfg.Options.TUI.Mouse {
		mouseLabel = "Enable Mouse"
	}
	commands = append(commands, NewCommandItem(c.com.Styles, "toggle_mouse", mouseLabel, "", ActionToggleMouseSupport{}))

	commands = append(
		commands,
		NewCommandItem(c.com.Styles, "quit", "Quit", km.Quit.Help().Key, tea.QuitMsg{}).WithAliases("exit"),
	)

	return commands
}

// SetCustomCommands sets the custom commands and refreshes the view if user commands are currently displayed.
func (c *Commands) SetCustomCommands(customCommands []commands.CustomCommand) {
	c.customCommands = customCommands
	if c.selected == UserCommands {
		c.setCommandItems(c.selected)
	}
}

// SetMCPPrompts sets the MCP prompts and refreshes the view if MCP prompts are currently displayed.
func (c *Commands) SetMCPPrompts(mcpPrompts []commands.MCPPrompt) {
	c.mcpPrompts = mcpPrompts
	if c.selected == MCPPrompts {
		c.setCommandItems(c.selected)
	}
}

// StartLoading implements [LoadingDialog].
func (c *Commands) StartLoading() tea.Cmd {
	if c.loading {
		return nil
	}
	c.loading = true
	return c.spinner.Tick
}

// StopLoading implements [LoadingDialog].
func (c *Commands) StopLoading() {
	c.loading = false
}
