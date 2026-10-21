package dialog

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/session"
)

// ActionClose is a message to close the current dialog.
type ActionClose struct{}

// ActionQuit is a message to quit the application.
type ActionQuit struct{}

// ActionOpenDialog is a message to open a dialog.
type ActionOpenDialog struct {
	DialogID ID
}

// ActionSelectSession is a message indicating a session has been selected.
type ActionSelectSession struct {
	Session session.Session
}

// ActionSelectModel is a message indicating a model has been selected.
type ActionSelectModel struct {
	Provider  catalog.Provider
	Model     config.SelectedModel
	ModelType config.SelectedModelType
}

type ActionConfigureProvider struct {
	ProviderID catalog.InferenceProvider
}

type ActionSaveProvider struct {
	ProviderID catalog.InferenceProvider
	Update     config.ProviderUpdate
}

type ActionRemoveProvider struct {
	ProviderID catalog.InferenceProvider
}

type ActionProviderConfigured struct {
	ProviderID catalog.InferenceProvider
}

// Messages for commands
type (
	ActionNewSession              struct{}
	ActionToggleHelp              struct{}
	ActionToggleThinking          struct{}
	ActionExternalEditor          struct{}
	ActionSelectNotificationStyle struct {
		Style string
	}
	ActionToggleMouseSupport struct{}
	ActionInitializeProject  struct{}
	// ActionCompact compacts (summarizes) the session, optionally steered
	// by focus instructions.
	ActionCompact struct {
		SessionID string
		Args      commands.Args
	}
	// ActionSetGoal sets the session goal and starts working toward it.
	ActionSetGoal struct {
		Args commands.Args
	}
	// ActionClearGoal clears the current session's goal.
	ActionClearGoal struct{}
	// ActionSaveSummary is a message to save the current session summary
	// to a markdown file in the data directory.
	ActionSaveSummary struct {
		SessionID string
	}
	// ActionExportConversation is a message to export the whole session
	// transcript to a markdown file in the data directory.
	ActionExportConversation struct {
		SessionID string
	}
	// ActionSelectReasoningEffort is a message indicating a reasoning effort
	// has been selected.
	ActionSelectReasoningEffort struct {
		Effort string
	}
	// ActionPreviewTheme is a message indicating the theme picker moved to
	// a new entry. The named theme is applied to the whole UI right away so
	// the user can see it, but not persisted: the picker restores the
	// previous theme if it closes without a selection. Cmd carries any
	// pending input command so previewing never swallows it.
	ActionPreviewTheme struct {
		Name string
		Cmd  tea.Cmd
	}
	// ActionSelectTheme is a message indicating a theme has been confirmed
	// in the theme picker and should be persisted.
	ActionSelectTheme struct {
		Name string
	}
	// ActionRunCustomCommand runs a custom command: a command file, a
	// skill or an extension command.
	ActionRunCustomCommand struct {
		Command commands.CustomCommand
		Args    commands.Args
	}
	// ActionRunMCPPrompt runs an MCP server's prompt.
	ActionRunMCPPrompt struct {
		Prompt commands.MCPPrompt
		Args   commands.Args
	}
	// ActionInvoke runs a command picked from the palette or typed in the
	// editor, with the raw text typed after its name. Arguments are bound
	// through the action's ArgSpec when it has one, and the arguments
	// form asks for any required field the text left out.
	ActionInvoke struct {
		Action Action
		Name   string
		Raw    string
	}
	// ActionInsertInvocation puts a command's name in the editor for the
	// user to follow with its arguments.
	ActionInsertInvocation struct {
		Name   string
		Action ArgAction
	}
	// ActionOpenArguments opens the arguments form for Action with what
	// was already typed filled in.
	ActionOpenArguments struct {
		Action ArgAction
		Args   commands.Args
	}
)

// ArgAction is an action that takes arguments. Its ArgSpec is the one
// description of them: the palette hint, the editor binding and the
// arguments form all read it, and WithArgs is the one way arguments
// reach the action.
type ArgAction interface {
	ArgSpec() commands.ArgSpec
	// WithArgs returns the action to run with args bound.
	WithArgs(args commands.Args) Action
}

var (
	_ ArgAction = ActionCompact{}
	_ ArgAction = ActionSetGoal{}
	_ ArgAction = ActionRunCustomCommand{}
	_ ArgAction = ActionRunMCPPrompt{}
)

// compactSpec is how /compact takes its optional focus.
var compactSpec = commands.ArgSpec{
	Fields: []commands.Argument{{
		ID:          "focus",
		Title:       "Focus",
		Description: "Optional: what the compacted summary should keep. Leave empty for a general summary.",
	}},
	Title:       "Compact Session",
	Description: "Optionally steer what the compacted summary keeps.",
}

// ArgSpec implements ArgAction.
func (ActionCompact) ArgSpec() commands.ArgSpec { return compactSpec }

// WithArgs implements ArgAction.
func (a ActionCompact) WithArgs(args commands.Args) Action {
	a.Args = args
	return a
}

// goalSpec is how /goal takes its condition.
var goalSpec = commands.ArgSpec{
	Fields: []commands.Argument{{
		ID:          "condition",
		Title:       "Goal",
		Description: "What must be true for the work to be done. Add \"or stop after 20 turns\" to bound it.",
		Required:    true,
	}},
	Hint:        "<condition> | clear",
	Title:       "Set Goal",
	Description: "The agent keeps working, across compactions, until a judge finds this met.",
}

// goalClearWords clear the goal instead of setting one ("/goal clear").
var goalClearWords = map[string]bool{
	"clear": true, "stop": true, "off": true, "reset": true, "none": true, "cancel": true,
}

// ArgSpec implements ArgAction.
func (ActionSetGoal) ArgSpec() commands.ArgSpec { return goalSpec }

// WithArgs implements ArgAction.
func (a ActionSetGoal) WithArgs(args commands.Args) Action {
	if goalClearWords[strings.ToLower(strings.TrimSpace(args.Value("condition")))] {
		return ActionClearGoal{}
	}
	a.Args = args
	return a
}

// ArgSpec implements ArgAction.
func (a ActionRunCustomCommand) ArgSpec() commands.ArgSpec { return a.Command.Spec() }

// WithArgs implements ArgAction.
func (a ActionRunCustomCommand) WithArgs(args commands.Args) Action {
	a.Args = args
	return a
}

// ArgSpec implements ArgAction.
func (a ActionRunMCPPrompt) ArgSpec() commands.ArgSpec { return a.Prompt.Spec() }

// WithArgs implements ArgAction.
func (a ActionRunMCPPrompt) WithArgs(args commands.Args) Action {
	a.Args = args
	return a
}

type IdleAction interface {
	Action
	RequiresIdle() string
}

var (
	_ IdleAction = ActionNewSession{}
	_ IdleAction = ActionCompact{}
	_ IdleAction = ActionSaveSummary{}
	_ IdleAction = ActionExternalEditor{}
	_ IdleAction = ActionInitializeProject{}
)

func (ActionNewSession) RequiresIdle() string        { return "starting a new session" }
func (ActionCompact) RequiresIdle() string           { return "compacting the session" }
func (ActionSaveSummary) RequiresIdle() string       { return "saving the summary" }
func (ActionExternalEditor) RequiresIdle() string    { return "opening the editor" }
func (ActionInitializeProject) RequiresIdle() string { return "initializing the project" }

// Messages for MCP OAuth authentication dialog.
type (
	// ActionMCPAuthStarted is sent when the user approves authentication
	// for an MCP server. The UI should initiate the actual auth flow
	// using the provided context, which the dialog will cancel if the
	// user closes it.
	ActionMCPAuthStarted struct {
		Name string
		Ctx  context.Context
	}

	// ActionMCPAuthComplete is sent when MCP authentication succeeds.
	ActionMCPAuthComplete struct {
		Name string
	}

	// ActionMCPAuthErrored is sent when MCP authentication fails.
	ActionMCPAuthErrored struct {
		Name  string
		Error error
	}
)

// Messages for OAuth2 device flow dialog.
type (
	// ActionInitiateOAuth is sent when the device auth is initiated
	// successfully.
	ActionInitiateOAuth struct {
		DeviceCode      string
		UserCode        string
		ExpiresIn       int
		VerificationURL string
		Interval        int
	}

	// ActionCompleteOAuth is sent when the device flow completes successfully.
	ActionCompleteOAuth struct {
		Token *oauth.Token
	}

	// ActionOAuthErrored is sent when the device flow encounters an error.
	ActionOAuthErrored struct {
		Error error
	}
)

// ActionLoadSubagentSession is a message to load a subagent's child session.
type ActionLoadSubagentSession struct {
	SessionID string
}

// ActionCmd represents an action that carries a [tea.Cmd] to be passed to the
// Bubble Tea program loop.
type ActionCmd struct {
	Cmd tea.Cmd
}

// ActionRun runs a command whose arguments are bound: what the palette,
// the editor and the arguments form all end in.
type ActionRun struct {
	Action Action
}

// isAction marks ActionClose as an [Action].
func (ActionClose) isAction() {}

// isAction marks ActionQuit as an [Action].
func (ActionQuit) isAction() {}

// isAction marks ActionOpenDialog as an [Action].
func (ActionOpenDialog) isAction() {}

// isAction marks ActionSelectSession as an [Action].
func (ActionSelectSession) isAction() {}

// isAction marks ActionSelectModel as an [Action].
func (ActionSelectModel) isAction() {}

// isAction marks ActionNewSession as an [Action].
func (ActionNewSession) isAction() {}

// isAction marks ActionToggleHelp as an [Action].
func (ActionToggleHelp) isAction() {}

// isAction marks ActionToggleThinking as an [Action].
func (ActionToggleThinking) isAction() {}

// isAction marks ActionExternalEditor as an [Action].
func (ActionExternalEditor) isAction() {}

// isAction marks ActionSelectNotificationStyle as an [Action].
func (ActionSelectNotificationStyle) isAction() {}

// isAction marks ActionToggleMouseSupport as an [Action].
func (ActionToggleMouseSupport) isAction() {}

// isAction marks ActionInitializeProject as an [Action].
func (ActionInitializeProject) isAction() {}

// isAction marks ActionCompact as an [Action].
func (ActionCompact) isAction() {}

// isAction marks ActionSetGoal as an [Action].
func (ActionSetGoal) isAction() {}

// isAction marks ActionClearGoal as an [Action].
func (ActionClearGoal) isAction() {}

// isAction marks ActionSaveSummary as an [Action].
func (ActionSaveSummary) isAction() {}

// isAction marks ActionExportConversation as an [Action].
func (ActionExportConversation) isAction() {}

// isAction marks ActionSelectReasoningEffort as an [Action].
func (ActionSelectReasoningEffort) isAction() {}

// isAction marks ActionPreviewTheme as an [Action].
func (ActionPreviewTheme) isAction() {}

// isAction marks ActionSelectTheme as an [Action].
func (ActionSelectTheme) isAction() {}

// isAction marks ActionRunCustomCommand as an [Action].
func (ActionRunCustomCommand) isAction() {}

// isAction marks ActionRunMCPPrompt as an [Action].
func (ActionRunMCPPrompt) isAction() {}

// isAction marks ActionInvoke as an [Action].
func (ActionInvoke) isAction() {}

// isAction marks ActionInsertInvocation as an [Action].
func (ActionInsertInvocation) isAction() {}

// isAction marks ActionOpenArguments as an [Action].
func (ActionOpenArguments) isAction() {}

// isAction marks ActionMCPAuthStarted as an [Action].
func (ActionMCPAuthStarted) isAction() {}

// isAction marks ActionMCPAuthComplete as an [Action].
func (ActionMCPAuthComplete) isAction() {}

// isAction marks ActionMCPAuthErrored as an [Action].
func (ActionMCPAuthErrored) isAction() {}

func (ActionConfigureProvider) isAction() {}

func (ActionSaveProvider) isAction() {}

func (ActionRemoveProvider) isAction() {}

func (ActionProviderConfigured) isAction() {}

// isAction marks ActionInitiateOAuth as an [Action].
func (ActionInitiateOAuth) isAction() {}

// isAction marks ActionCompleteOAuth as an [Action].
func (ActionCompleteOAuth) isAction() {}

// isAction marks ActionOAuthErrored as an [Action].
func (ActionOAuthErrored) isAction() {}

// isAction marks ActionLoadSubagentSession as an [Action].
func (ActionLoadSubagentSession) isAction() {}

// isAction marks ActionCmd as an [Action].
func (ActionCmd) isAction() {}

// isAction marks ActionRun as an [Action].
func (ActionRun) isAction() {}

// isAction marks ActionLSPRestart as an [Action].
func (ActionLSPRestart) isAction() {}

// isAction marks ActionLSPSetSessionDisabled as an [Action].
func (ActionLSPSetSessionDisabled) isAction() {}

// isAction marks ActionMCPReconnect as an [Action].
func (ActionMCPReconnect) isAction() {}

// isAction marks ActionMCPDisableForSession as an [Action].
func (ActionMCPDisableForSession) isAction() {}

// isAction marks ActionOpenMCPAuth as an [Action].
func (ActionOpenMCPAuth) isAction() {}

// isAction marks ActionMentionSelected as an [Action].
func (ActionMentionSelected) isAction() {}

// isAction marks ActionMentionCancelled as an [Action].
func (ActionMentionCancelled) isAction() {}

// isAction marks ActionRewindConfirmed as an [Action].
func (ActionRewindConfirmed) isAction() {}
