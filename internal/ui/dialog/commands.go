package dialog

import (
	"os"
	"strings"

	"charm.land/bubbles/v2/help"
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
const CommandsID = "commands"

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

	help  help.Model
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

	help := help.New()
	help.Styles = com.Styles.DialogHelpStyles()

	c.help = help

	c.list = list.NewFilterableList()
	c.list.Focus()
	c.list.SetSelected(0)

	c.input = textinput.New()
	c.input.SetVirtualCursor(false)
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
func (c *Commands) ID() string {
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
			if !c.skillsOnly && (len(c.customCommands) > 0 || len(c.mcpPrompts) > 0) {
				c.selected = c.nextCommandType()
				c.setCommandItems(c.selected)
			}
		case key.Matches(msg, c.keyMap.ShiftTab):
			if !c.skillsOnly && (len(c.customCommands) > 0 || len(c.mcpPrompts) > 0) {
				c.selected = c.previousCommandType()
				c.setCommandItems(c.selected)
			}
		default:
			var cmd tea.Cmd
			for _, item := range c.list.FilteredItems() {
				if item, ok := item.(*CommandItem); ok && item != nil {
					if msg.String() == item.Shortcut() {
						return item.Action()
					}
				}
			}
			prevValue := c.input.Value()
			c.input, cmd = c.input.Update(msg)
			value := c.input.Value()
			if value != prevValue {
				c.list.SetFilter(value)
				c.list.ScrollToTop()
				c.list.SetSelected(0)
			}
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
func commandsRadioView(sty *styles.Styles, selected CommandType, hasUserCmds bool, hasMCPPrompts bool) string {
	if !hasUserCmds && !hasMCPPrompts {
		return ""
	}

	selectedFn := func(t CommandType) string {
		if t == selected {
			return sty.Radio.On.Padding(0, 1).Render() + sty.Radio.Label.Render(t.String())
		}
		return sty.Radio.Off.Padding(0, 1).Render() + sty.Radio.Label.Render(t.String())
	}

	parts := []string{
		selectedFn(SystemCommands),
	}

	if hasUserCmds {
		parts = append(parts, selectedFn(UserCommands))
	}
	if hasMCPPrompts {
		parts = append(parts, selectedFn(MCPPrompts))
	}

	return strings.Join(parts, " ")
}

// Draw implements [Dialog].
func (c *Commands) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := c.com.Styles
	width := max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	height := max(0, min(defaultDialogHeight, area.Dy()-t.Dialog.View.GetVerticalBorderSize()))
	if area.Dx() != c.windowWidth && c.selected == SystemCommands {
		c.windowWidth = area.Dx()
		// since some items in the list depend on width (e.g. toggle sidebar command),
		// we need to reset the command items when width changes
		c.setCommandItems(c.selected)
	}

	innerWidth := width - c.com.Styles.Dialog.View.GetHorizontalFrameSize()
	heightOffset := t.Dialog.Title.GetVerticalFrameSize() + titleContentHeight +
		t.Dialog.InputPrompt.GetVerticalFrameSize() + inputContentHeight +
		t.Dialog.HelpView.GetVerticalFrameSize() +
		t.Dialog.View.GetVerticalFrameSize()

	c.input.SetWidth(dialogInputTextWidth(t, c.input, innerWidth))

	c.list.SetSize(innerWidth, max(0, height-heightOffset))

	// Hide the shortcut hints uniformly when the widest would crowd names.
	applyInfoColumnVisibility(c.list.FilteredItems(), innerWidth, commandInfoMaxPercent)

	rc := NewRenderContext(t, width)
	rc.Title = "Commands"
	if c.skillsOnly {
		rc.Title = "Skills"
	} else {
		rc.TitleInfo = commandsRadioView(t, c.selected, len(c.customCommands) > 0, len(c.mcpPrompts) > 0)
	}
	inputView := t.Dialog.InputPrompt.Render(c.input.View())
	rc.AddPart(inputView)
	listView := t.Dialog.List.Height(c.list.Height()).Render(c.list.Render())
	rc.AddPart(listView)
	rc.Help = renderDialogHelp(t, &c.help, c, innerWidth)

	if c.loading {
		rc.Help = t.Dialog.HelpView.Width(innerWidth).Render(c.spinner.View() + " Generating Prompt...")
	}

	view := rc.Render()

	cur := c.Cursor()
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (c *Commands) ShortHelp() []key.Binding {
	return []key.Binding{
		c.keyMap.Tab,
		c.keyMap.UpDown,
		c.keyMap.Select,
		c.keyMap.Close,
	}
}

// FullHelp implements [help.KeyMap].
func (c *Commands) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{c.keyMap.Select, c.keyMap.Next, c.keyMap.Previous, c.keyMap.Tab},
		{c.keyMap.Close},
	}
}

// nextCommandType returns the next command type in the cycle.
func (c *Commands) nextCommandType() CommandType {
	switch c.selected {
	case SystemCommands:
		if len(c.customCommands) > 0 {
			return UserCommands
		}
		if len(c.mcpPrompts) > 0 {
			return MCPPrompts
		}
		fallthrough
	case UserCommands:
		if len(c.mcpPrompts) > 0 {
			return MCPPrompts
		}
		fallthrough
	case MCPPrompts:
		return SystemCommands
	default:
		return SystemCommands
	}
}

// previousCommandType returns the previous command type in the cycle.
func (c *Commands) previousCommandType() CommandType {
	switch c.selected {
	case SystemCommands:
		if len(c.mcpPrompts) > 0 {
			return MCPPrompts
		}
		if len(c.customCommands) > 0 {
			return UserCommands
		}
		return SystemCommands
	case UserCommands:
		return SystemCommands
	case MCPPrompts:
		if len(c.customCommands) > 0 {
			return UserCommands
		}
		return SystemCommands
	case SkillsCommands:
		return SkillsCommands
	default:
		return SystemCommands
	}
}

// setCommandItems sets the command items based on the specified command type.
func (c *Commands) setCommandItems(commandType CommandType) {
	c.selected = commandType

	commandItems := []list.FilterableItem{}
	switch c.selected {
	case SystemCommands:
		for _, cmd := range c.defaultCommands() {
			commandItems = append(commandItems, cmd)
		}
	case UserCommands:
		for _, cmd := range c.customCommands {
			// Skills live in their own palette behind "/".
			if cmd.Skill != nil {
				continue
			}
			action := ActionRunCustomCommand{
				Content:     cmd.Content,
				Arguments:   cmd.Arguments,
				Skill:       cmd.Skill,
				ExtensionID: cmd.ExtensionID,
			}
			item := NewCommandItem(c.com.Styles, "custom_"+cmd.ID, cmd.Name, "", action)
			if cmd.Description != "" {
				item = item.WithDescription(cmd.Description)
			}
			commandItems = append(commandItems, item)
		}
	case MCPPrompts:
		for _, cmd := range c.mcpPrompts {
			action := ActionRunMCPPrompt{
				Title:       cmd.Title,
				Description: cmd.Description,
				PromptID:    cmd.PromptID,
				ClientID:    cmd.ClientID,
				Arguments:   cmd.Arguments,
			}
			commandItems = append(commandItems, NewCommandItem(c.com.Styles, "mcp_"+cmd.ID, cmd.PromptID, "", action))
		}
	case SkillsCommands:
		for _, cmd := range c.customCommands {
			if cmd.Skill == nil {
				continue
			}
			action := ActionAttachSkill{ID: cmd.Skill.SkillFilePath, Name: cmd.Skill.Name}
			item := NewCommandItem(c.com.Styles, "custom_"+cmd.ID, cmd.Name, "", action)
			item = item.WithDescription(cmd.Skill.Description)
			commandItems = append(commandItems, item)
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
func (c *Commands) defaultCommands() []*CommandItem {
	commands := []*CommandItem{
		NewCommandItem(c.com.Styles, "new_session", "New Session", "ctrl+n", ActionNewSession{}).WithAliases("clear"),
		NewCommandItem(c.com.Styles, "switch_session", "Sessions", "ctrl+s", ActionOpenDialog{SessionsID}).WithAliases("resume", "switch"),
		NewCommandItem(c.com.Styles, "switch_model", "Switch Model", "ctrl+l", ActionOpenDialog{ModelsID}),
		NewCommandItem(c.com.Styles, "connect_provider", "Connect Provider", "", ActionOpenDialog{ConnectID}).WithAliases("provider", "auth", "login"),
		NewCommandItem(c.com.Styles, "switch_theme", "Switch Theme", "alt+t", ActionOpenDialog{ThemesID}),
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
		commands = append(commands, NewCommandItem(c.com.Styles, "export_conversation", "Export Conversation", "ctrl+shift+e", ActionExportConversation{SessionID: c.sessionID}).WithAliases("export", "transcript", "copy"))
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
	if c.hasSession {
		cfgPrime := c.com.Config()
		agentCfg := cfgPrime.Agents[config.AgentCoder]
		model := cfgPrime.GetModelByType(agentCfg.Model)
		if model != nil && model.SupportsImages {
			commands = append(commands, NewCommandItem(c.com.Styles, "file_picker", "Open File Picker", "ctrl+f", ActionOpenDialog{
				DialogID: FilePickerID,
			}))
		}
	}

	// Add external editor command if $EDITOR is available.
	//
	// TODO: Use [tea.EnvMsg] to get environment variable instead of os.Getenv;
	// because os.Getenv does IO is breaks the TEA paradigm and is generally an
	// antipattern.
	if os.Getenv("EDITOR") != "" {
		commands = append(commands, NewCommandItem(c.com.Styles, "open_external_editor", "Open External Editor", "ctrl+o", ActionExternalEditor{}))
	}

	if c.hasTodos {
		commands = append(commands, NewCommandItem(c.com.Styles, "toggle_pills", "Toggle To-Dos", "ctrl+t", ActionTogglePills{}))
	}

	// Add a command for selecting notification style via picker dialog.
	notificationLabel := "Notification Style"
	commands = append(commands, NewCommandItem(c.com.Styles, "select_notifications", notificationLabel, "", ActionOpenDialog{DialogID: NotificationsID}))

	commands = append(
		commands,
		NewCommandItem(c.com.Styles, "toggle_help", "Toggle Help", "ctrl+g", ActionToggleHelp{}),
		NewCommandItem(c.com.Styles, "init", "Initialize Project", "", ActionInitializeProject{}),
	)

	// Add transparent background toggle.
	transparentLabel := "Disable Background Color"
	if cfg != nil && cfg.Options != nil && cfg.Options.TUI.IsTransparent() {
		transparentLabel = "Enable Background Color"
	}
	commands = append(commands, NewCommandItem(c.com.Styles, "toggle_transparent", transparentLabel, "", ActionToggleTransparentBackground{}))

	// Add mouse support toggle.
	mouseLabel := "Disable Mouse"
	if cfg != nil && cfg.Options != nil && cfg.Options.TUI.Mouse != nil && !*cfg.Options.TUI.Mouse {
		mouseLabel = "Enable Mouse"
	}
	commands = append(commands, NewCommandItem(c.com.Styles, "toggle_mouse", mouseLabel, "", ActionToggleMouseSupport{}))

	commands = append(
		commands,
		NewCommandItem(c.com.Styles, "quit", "Quit", "ctrl+c", tea.QuitMsg{}).WithAliases("exit"),
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
