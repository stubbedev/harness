package dialog

import (
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
)

// CommandState is the session state that decides which built-in
// commands apply.
type CommandState struct {
	// SessionID is the current session, empty on the landing screen.
	SessionID  string
	HasSummary bool
	HasGoal    bool
}

func (s CommandState) hasSession() bool { return s.SessionID != "" }

// Catalog is every command there is to run: the built-ins that apply to
// the current state, loaded custom commands and skills, and MCP prompts.
// The palette lists it and the editor resolves typed names against it,
// so a command is reachable the same way from both.
type Catalog struct {
	com            *common.Common
	state          CommandState
	customCommands []commands.CustomCommand
	mcpPrompts     []commands.MCPPrompt
}

// NewCatalog returns the catalog for state.
func NewCatalog(com *common.Common, state CommandState, customCommands []commands.CustomCommand, mcpPrompts []commands.MCPPrompt) *Catalog {
	return &Catalog{com: com, state: state, customCommands: customCommands, mcpPrompts: mcpPrompts}
}

// Invocables returns every command the editor can invoke by name, in
// the order a name resolves in (see menus).
func (c *Catalog) Invocables() []*CommandItem {
	var items []*CommandItem
	for _, m := range c.menus() {
		items = append(items, m.items()...)
	}
	return items
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
type systemMenu struct{ c *Catalog }

func (systemMenu) tab() CommandType        { return SystemCommands }
func (systemMenu) hasItems() bool          { return true }
func (m systemMenu) items() []*CommandItem { return m.c.defaultCommands() }

// customMenu is a page of loaded commands: the skills page, or the
// user-defined page of command files and extension commands.
type customMenu struct {
	c      *Catalog
	skills bool
}

func (m customMenu) tab() CommandType {
	if m.skills {
		return SkillsCommands
	}
	return UserCommands
}

func (m customMenu) hasItems() bool { return len(m.commands()) > 0 }

// commands returns the loaded commands this page lists.
func (m customMenu) commands() []commands.CustomCommand {
	var out []commands.CustomCommand
	for _, cmd := range m.c.customCommands {
		if (cmd.Skill != nil) == m.skills {
			out = append(out, cmd)
		}
	}
	return out
}

func (m customMenu) items() []*CommandItem {
	var items []*CommandItem
	for _, cmd := range m.commands() {
		names := commands.InvocationNames(cmd.Name)
		item := NewCommandItem(m.c.com.Styles, "custom_"+cmd.ID, cmd.Name, "", ActionRunCustomCommand{Command: cmd}).
			WithSlashNames(names...).
			// The source prefix (project:/user:/system:) labels where the
			// command lives, not its name; match on the name after it.
			WithFilterTitle(names[0])
		description := cmd.Description
		if cmd.Skill != nil {
			description = cmd.Skill.Description
		}
		if description != "" {
			item = item.WithDescription(description)
		}
		items = append(items, item)
	}
	return items
}

// mcpMenu is the MCP prompt page.
type mcpMenu struct{ c *Catalog }

func (mcpMenu) tab() CommandType { return MCPPrompts }
func (m mcpMenu) hasItems() bool { return len(m.c.mcpPrompts) > 0 }
func (m mcpMenu) items() []*CommandItem {
	var items []*CommandItem
	for _, prompt := range m.c.mcpPrompts {
		item := NewCommandItem(m.c.com.Styles, "mcp_"+prompt.ID, prompt.PromptID, "", ActionRunMCPPrompt{Prompt: prompt}).
			WithSlashNames(prompt.ID)
		if prompt.Description != "" {
			item = item.WithDescription(prompt.Description)
		}
		items = append(items, item)
	}
	return items
}

// menus returns the palette's content sources in tab order, which is
// also the order a typed name resolves in: a built-in cannot be shadowed
// by a skill, nor a skill by a command file.
func (c *Catalog) menus() []commandMenu {
	return []commandMenu{systemMenu{c}, customMenu{c: c, skills: true}, customMenu{c: c}, mcpMenu{c}}
}

// defaultCommands returns the list of default system commands.
// defaultCommands returns the list of default system commands. Shortcut
// labels follow the keymap: a rebound action shows its new key here, and
// the palette matches the label literally, so the two never drift apart.
func (c *Catalog) defaultCommands() []*CommandItem {
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
	if c.state.hasSession() {
		commands = append(commands, NewCommandItem(c.com.Styles, "summarize", "Summarize Session", "", ActionSummarize{SessionID: c.state.SessionID}))
		commands = append(commands, NewCommandItem(c.com.Styles, "rewind", "Rewind to Earlier Turn", "", ActionOpenDialog{RewindID}))
		commands = append(commands, NewCommandItem(c.com.Styles, "compact", "Compact Session", "", ActionCompact{SessionID: c.state.SessionID}))
	}

	commands = append(commands, NewCommandItem(c.com.Styles, "goal", "Set Goal", "", ActionSetGoal{}))
	if c.state.hasSession() && c.state.HasGoal {
		commands = append(commands, NewCommandItem(c.com.Styles, "clear_goal", "Clear Goal", "", ActionClearGoal{}))
	}

	// Only show the export command when there is a conversation to export
	if c.state.hasSession() {
		commands = append(commands, NewCommandItem(c.com.Styles, "export_conversation", "Export Conversation", km.ExportConversation.Help().Key, ActionExportConversation{SessionID: c.state.SessionID}).WithAliases("export", "transcript", "copy"))
	}

	// Only show the save summary command if the session already has one
	if c.state.hasSession() && c.state.HasSummary {
		commands = append(commands, NewCommandItem(c.com.Styles, "save_summary", "Save Session Summary", "", ActionSaveSummary{SessionID: c.state.SessionID}))
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
