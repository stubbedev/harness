package model

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"maps"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/ultraviolet/layout"
	"github.com/charmbracelet/ultraviolet/screen"
	"github.com/charmbracelet/x/editor"
	xstrings "github.com/charmbracelet/x/exp/strings"
	"github.com/stubbedev/harness/internal/agent/notify"
	agenttools "github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/app"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/clipboard"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/event"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/home"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/skills"
	"github.com/stubbedev/harness/internal/stringext"
	"github.com/stubbedev/harness/internal/subagents"
	"github.com/stubbedev/harness/internal/ui/attachments"
	"github.com/stubbedev/harness/internal/ui/chat"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/completions"
	"github.com/stubbedev/harness/internal/ui/dialog"
	fimage "github.com/stubbedev/harness/internal/ui/image"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/notification"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

// If pasted text has more than 10 newlines, treat it as a file attachment.
const pasteLinesThreshold = 10

// If pasted text has more than 1000 columns, treat it as a file attachment.
const pasteColsThreshold = 1000

// Session details panel max height.
const sessionDetailsMaxHeight = 20

// TextareaMaxHeight is the maximum height of the prompt textarea.
const TextareaMaxHeight = 15

// editorHeightMargin is the height of the attachments strip rendered
// above the textarea while it has pills; the editor reserves it only
// then, so with no attachments the frame hugs the textarea.
const editorHeightMargin = 1

// editorFrameRows is the height of the editor's frame: one rule line
// above the content and one below it. drawEditorArea draws them and
// generateLayout reserves them; editorContentOrigin derives the
// content's top from the same knowledge.
const editorFrameRows = 2

// TextareaMinHeight is the minimum height of the prompt textarea when
// options.tui.textarea_min_height is unset; the live value comes from
// TUIOptions.MinTextareaHeight.
const TextareaMinHeight = config.DefaultTextareaMinHeight

// uiFocusState represents the current focus state of the UI.
type uiFocusState uint8

// Possible uiFocusState values.
const (
	uiFocusNone uiFocusState = iota
	uiFocusEditor
	uiFocusMain
	uiFocusTasks
)

type uiState uint8

// Possible uiState values.
const (
	uiOnboarding uiState = iota
	uiLanding
	uiChat
)

type openEditorMsg struct {
	Text string
}

// rewindSession performs the rewind on a background command and, for
// modes that restore the conversation, refills the editor with the
// rewound prompt so it can be edited and re-sent. It mirrors
// exportConversationToFile's error reporting.
func (m *UI) rewindSession(sessionID, messageID, prompt string, mode checkpoints.Mode) tea.Cmd {
	return func() tea.Msg {
		err := m.com.Workspace.Rewind(context.Background(), sessionID, messageID, mode)
		if err != nil {
			return util.CmdHandler(util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("Rewind failed: %v", err),
			})()
		}
		if mode != checkpoints.ModeFiles {
			return tea.Sequence(
				func() tea.Msg { return openEditorMsg{Text: prompt} },
				util.ReportInfo("Rewound. Resend or edit the restored prompt."),
			)()
		}
		return util.ReportInfo("Working tree restored from checkpoint.")()
	}
}

type shellResultMsg struct {
	PendingID string // ID of the pending ShellItem to update.
	Command   string
	Output    string
	ExitCode  int
}

// shellStreamMsg carries incremental output from a streaming shell command.
type shellStreamMsg struct {
	PendingID string
	Chunk     string
	streamCh  <-chan string // unexported; used to continue draining
}

type (
	// cancelTimerExpiredMsg is sent when the cancel timer expires.
	cancelTimerExpiredMsg struct{}
	// quitTimerExpiredMsg is sent when the quit timer expires.
	quitTimerExpiredMsg struct{}
	// userCommandsLoadedMsg is sent when user commands are loaded.
	userCommandsLoadedMsg struct {
		Commands []commands.CustomCommand
	}
	// mcpPromptsLoadedMsg is sent when mcp prompts are loaded.
	mcpPromptsLoadedMsg struct {
		Prompts []commands.MCPPrompt
	}
	// mcpStateChangedMsg is sent when there is a change in MCP client states.
	mcpStateChangedMsg struct {
		states map[string]mcp.ClientInfo
	}
	// sendMessageMsg is sent to send a message.
	// currently only used for mcp prompts.
	sendMessageMsg struct {
		Content     string
		Attachments []message.Attachment
	}

	// closeDialogMsg is sent to close the current dialog.
	closeDialogMsg struct{}

	// copyChatHighlightMsg is sent to copy the current chat highlight to clipboard.
	copyChatHighlightMsg struct{}

	// sessionFilesUpdatesMsg is sent when the files for this session have been updated
	sessionFilesUpdatesMsg struct {
		sessionFiles []SessionFile
	}
	// parentTitleMsg is sent when the parent session metadata has been
	// fetched: the title for the breadcrumb and this child's subagent color.
	parentTitleMsg struct {
		// forSession is the child session the fetch was scoped to; a result
		// that raced a session switch (the user navigated away before it
		// resolved) is discarded rather than applied, mirroring
		// promptQueueMsg.forSession.
		forSession string
		title      string
		color      string
	}

	// runningSubagentsMsg carries the refreshed running-subagent list,
	// resolved off the Update path to keep DB IO out of the message loop.
	runningSubagentsMsg struct {
		// forSession is the session the fetch was scoped to; a result that
		// raced a session switch (the user navigated away before this
		// resolved) is discarded rather than applied, mirroring
		// promptQueueMsg.forSession.
		forSession string
		list       []workspace.RunningSubagentInfo
	}
)

// UI represents the main user interface model.
type UI struct {
	com          *common.Common
	session      *session.Session
	sessionFiles []SessionFile

	// keeps track of read files while we don't have a session id
	sessionFileReads []string

	// initialSessionID is set when loading a specific session on startup.
	initialSessionID string
	// continueLastSession is set to continue the most recent session on startup.
	continueLastSession bool

	lastUserMessageTime int64

	// The width and height of the terminal in cells.
	width  int
	height int
	layout uiLayout

	isTransparent bool

	// mouseEnabled controls whether Bubble Tea mouse reporting is active.
	// When false, the terminal emulator (or tmux) handles text selection,
	// copy/paste, right-click, and scrolling instead of Harness.
	mouseEnabled bool

	// themeKey identifies the currently applied theme so applyTheme can
	// skip the expensive style rebuild when switching to a provider that
	// resolves to the same theme.
	themeKey string

	// themePreview is live only while the theme picker is open; see
	// [themePreview] and [UI.previewTheme].
	themePreview themePreview

	focus uiFocusState
	state uiState

	// Frame memoization (see framecache.go). scrollOnlyUpdate is set by
	// handlers that change nothing but the chat scroll position; frameDirty
	// overrides it when a layout change happens in the same update.
	frames           *frameCache
	scrollOnlyUpdate bool
	frameDirty       bool
	frameSkipPut     bool
	frameGCArmed     bool

	keyMap KeyMap
	keyenh tea.KeyboardEnhancementsMsg

	dialog *dialog.Overlay
	status *Status

	// isCanceling tracks whether the user has pressed escape once to cancel.
	isCanceling bool
	// rewindEscArmed tracks the first escape of the idle double-escape
	// that opens the rewind picker (the cancel-last-message path). It
	// shares the cancel timer window and is mutually exclusive with
	// isCanceling: while the agent is busy escape cancels the turn
	// instead.
	rewindEscArmed bool

	// isQuitting tracks whether the user has pressed the quit key once,
	// arming the double-press quit window.
	isQuitting bool

	// bangMode tracks whether the editor is in bang (!) shell mode.
	bangMode     bool
	bangWasEmpty bool // true when bang prompt became empty on last keystroke

	// pendingBangCommand holds a shell command that was issued before
	// the session finished loading. The loadSessionMsg handler creates
	// the pending UI item and starts execution once the chat list is
	// stable, eliminating races between session load and shell output.
	pendingBangCommand string

	// bangCancel cancels a running bang-mode shell command. Nil when no
	// bang command is in progress. Set by runShellCommand, cleared by
	// shellResultMsg. Checked by isAgentBusy and cancelAgent so that
	// Escape works for bang commands the same way it does for agent runs.
	bangCancel context.CancelFunc

	header *header

	// sendProgressBar instructs the TUI to send progress bar updates to the
	// terminal.
	sendProgressBar    bool
	progressBarEnabled bool

	// caps hold different terminal capabilities that we query for.
	caps common.Capabilities

	// Editor components
	textarea textarea.Model

	// textareaMouseSelecting tracks whether a mouse selection gesture is
	// currently in progress within the textarea (left button held after a
	// click inside the textarea region).
	textareaMouseSelecting bool

	// Active inline editor replaces the textarea when non-nil.
	activeInline dialog.InlineEditor
	// inlineCursor stores the cursor from the last inline editor
	// Draw call, used by the cursor positioning logic below.
	inlineCursor *tea.Cursor

	// Attachment list
	attachments *attachments.Attachments

	// pastedAttachments holds pastes referenced by the editor's inline
	// placeholder tokens, keyed by the token text. See paste.go.
	pastedAttachments map[string]message.Attachment

	readyPlaceholder   string
	workingPlaceholder string

	// Completions state
	completions              *completions.Completions
	completionsOpen          bool
	completionsStartIndex    int
	completionsQuery         string
	completionsPositionStart image.Point // x,y where user typed '@'

	// Chat components
	chat *Chat

	// lspStates / lspDiagnostics memoize the workspace LSP state and
	// per-server severity counts (each probe behind them is a synchronous
	// HTTP round-trip in client/server mode, and the sidebar, landing view,
	// and compact header render them every frame). LSP events refresh them
	// off-thread with a TTL backstop; see lsp.go.
	lspStates        map[string]workspace.LSPClientInfo
	lspDiagnostics   map[string]lsp.DiagnosticCounts
	lspFetchInFlight bool
	// lspRefreshQueued records that an LSP event arrived while a fetch was
	// already in flight; applyLSPStates re-dispatches so the freshest state
	// still lands.
	lspRefreshQueued bool
	lspCheckedAt     time.Time

	// mcp
	mcpStates map[string]mcp.ClientInfo

	// skills
	skillStates []*skills.SkillState

	// runningSubagents holds the live subagent list for the current session,
	// refreshed on each RuntimeEvent.
	runningSubagents []workspace.RunningSubagentInfo

	// knownChildSessionIDs accumulates child (subagent) session IDs seen via
	// subagents.RuntimeEvent for the currently-viewed session. Gates live
	// history.File events in handleFileEvent (internal/ui/model/session.go)
	// without a DB round trip per event. Entries are only added, never
	// removed, so a child that already finished is still recognized for
	// file events that arrive just after RuntimeEvent's Finished drops it
	// from Entries.
	knownChildSessionIDs map[string]bool

	// Subagents — cached at init, static for session lifetime.
	activeSubagentItems []completions.SubagentCompletionValue
	activeSubagentNames map[string]bool

	// Notification state
	notifyBackend       notification.Backend
	notifyWindowFocused bool
	// custom commands & mcp commands
	customCommands []commands.CustomCommand
	mcpPrompts     []commands.MCPPrompt

	// pills state
	pillsExpanded     bool
	pillsAutoExpanded bool

	// Background tasks (subagents) render in a strip under the chat, not
	// in the transcript. agentTasks is insertion-ordered; taskRows maps
	// rendered strip rows back to tasks for click handling.
	agentTasks     []*agentTask
	expandedTaskID string
	taskRows       []taskRow
	taskSpinner    spinner.Model
	tasksView      string
	// reapedAgentTasks records dispatches whose task was already reaped
	// (result landed or terminal runtime status). Assistant-message
	// updates keep arriving after that — the step-finish update publishes
	// after its tool results — and would otherwise resurrect the finished
	// dispatch as a running task. Tool call IDs are unique per dispatch,
	// so entries survive session switches without colliding.
	reapedAgentTasks map[string]bool
	// lastTasksReconcile paces the periodic running-list refresh while
	// tasks spin (see the spinner tick handler): a terminal RuntimeEvent
	// lost in flight would otherwise leave a spinner that nothing ever
	// settles, because no further event for that child arrives.
	lastTasksReconcile time.Time
	// taskCursor / taskSubCursor drive keyboard navigation of the
	// strip: taskCursor indexes the visible entries, taskSubCursor the
	// cursor task's nested calls (-1 = on the task row itself).
	taskCursor    int
	taskSubCursor int
	// lastTaskFocusID is the task the cursor last rested on while the
	// strip was focused, so focus can return to it after reaps shift
	// the indices.
	lastTaskFocusID string
	// promptQueue / promptQueueItems mirror the session's queued prompts.
	// They are event-driven with a TTL backstop, fetched off-thread by
	// dispatchPromptQueueRefresh (see workspace_cache.go); promptQueue is
	// always len(promptQueueItems).
	promptQueue          int
	promptQueueItems     []string
	promptQueueCheckedAt time.Time
	promptQueueInFlight  bool
	// promptQueueGen is bumped by every queue state transition; an
	// in-flight fetch captures it at dispatch and its result is discarded
	// if the generation has moved on (see workspace_cache.go).
	promptQueueGen uint64
	// pendingModelAction holds a model or reasoning-effort selection made
	// while the agent was mid-turn. The in-flight turn keeps the model it
	// started with; the queued action is re-dispatched when the turn
	// finishes, so the choice applies at the next turn.
	pendingModelAction tea.Msg
	// queuedPromptItem is the single transcript placeholder for prompts
	// queued behind the running turn (see queued_prompts.go), joining
	// every queued prompt into one entry. It is UI-local: nothing
	// persists until the agent dequeues the queue.
	queuedPromptItem *chat.QueuedMessageItem
	queuedPrompts    []string
	queuedPromptSeq  int
	// agentBusyCache memoizes the workspace busy
	// probes (synchronous HTTP round-trips in client/server mode). Reads
	// never probe; refreshes happen off-thread (see workspace_cache.go).
	agentBusyCache    ttlCache
	busyFetchInFlight bool
	// agentReady / agentModel memoize the coordinator readiness and
	// selected model (AgentIsReady/AgentModel are synchronous HTTP GETs in
	// client/server mode, and modelInfo renders them every frame). Seeded
	// once at construction and refreshed by the same off-thread probe as
	// agentBusyCache.
	agentReady bool
	agentModel workspace.AgentModel
	// busyFetchGen is bumped by every busy/permission state transition;
	// like promptQueueGen it lets a stale in-flight probe result be
	// discarded and re-fetched instead of clobbering newer state.
	busyFetchGen uint64
	pillsView    string

	// Todo spinner
	todoSpinner    spinner.Model
	todoIsSpinning bool

	// retryNotice tracks whether the status bar currently shows a
	// provider-retry notice. It is set on TypeAgentRetrying and
	// cleared on the next sign of progress (message, new turn,
	// session switch) so a stale backoff note never lingers.
	retryNotice bool

	// mouse highlighting related state
	lastClickTime time.Time
	hoverX        int
	hoverY        int

	// Prompt history for up/down navigation through previous messages.
	promptHistory struct {
		messages []string
		index    int
		draft    string
	}

	// parentTitle holds the resolved parent session title for the breadcrumb.
	// subagentColor holds this child session's subagent color, looked up from
	// the runtime when the session loads.
	parentTitle   string
	subagentColor string
}

// New creates a new instance of the [UI] model.
func New(com *common.Common, initialSessionID string, continueLast bool) *UI {
	// The keymap is built before the components that copy bindings out of
	// it (the textarea's select-all, the chat items' copy/scroll keys), so
	// user overrides from options.tui.keybinds reach every consumer.
	keyMap := *com.KeyMap()
	// Floating dialogs anchor to the bottom edge (which-key style) unless
	// the user asked for the top-floating noice.nvim placement.
	dialog.InstallPlacement(com.Config().Options.TUI.DialogPlacement)

	// Editor components
	ta := textarea.New()
	ta.SetStyles(com.Styles.Editor.Textarea)
	ta.ShowLineNumbers = false
	ta.CharLimit = -1
	ta.SetVirtualCursor(false)
	ta.DynamicHeight = true
	ta.MinHeight = com.Config().Options.TUI.MinTextareaHeight()
	ta.MaxHeight = TextareaMaxHeight
	// Keep "ctrl+a" for line-start (the textarea default); bind select-all
	// to "ctrl+shift+a" instead (line-start is also available via "home").
	ta.KeyMap.LineStart = keyMap.Editor.LineStart
	ta.KeyMap.SelectAll = keyMap.Editor.SelectAll
	// Copying is handled by harness's keymap (Editor.CopySelection) so it can
	// use harness's clipboard backend and user feedback; disable the
	// textarea's built-in copy binding.
	ta.KeyMap.CopySelection = key.NewBinding()
	ta.Focus()

	ch := NewChat(com, com.Config().Options.TUI.Scrollbar)
	ch.SetItemKeymap(chat.ItemKeymap{
		Copy:        keyMap.Chat.Copy,
		ScrollLeft:  keyMap.Chat.ScrollLeft,
		ScrollRight: keyMap.Chat.ScrollRight,
	})

	// Completions component
	comp := completions.New(
		com.Styles.Completions.Normal,
		com.Styles.Completions.Focused,
		com.Styles.Completions.Match,
	)
	todoSpinner := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(com.Styles.Pills.TodoSpinner),
	)
	taskSpinner := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(com.Styles.Pills.TodoSpinner),
	)

	// Attachments component
	attachments := attachments.New(
		attachments.NewRenderer(
			com.Styles.Attachments.Normal,
			com.Styles.Attachments.Deleting,
			com.Styles.Attachments.Image,
			com.Styles.Attachments.Text,
			com.Styles.Attachments.Skill,
			com.Styles.Attachments.Remove,
		),
		attachments.Keymap{
			DeleteMode: keyMap.Editor.AttachmentDeleteMode,
			DeleteAll:  keyMap.Editor.DeleteAllAttachments,
			Escape:     keyMap.Editor.Escape,
		},
	)

	header := newHeader(com)

	ui := &UI{
		com:                 com,
		dialog:              dialog.NewOverlay(),
		keyMap:              keyMap,
		textarea:            ta,
		chat:                ch,
		header:              header,
		completions:         comp,
		attachments:         attachments,
		todoSpinner:         todoSpinner,
		taskSpinner:         taskSpinner,
		frames:              newFrameCache(frameCacheTTL, frameCacheMaxEntries),
		lspStates:           make(map[string]workspace.LSPClientInfo),
		mcpStates:           make(map[string]mcp.ClientInfo),
		notifyBackend:       notification.NoopBackend{},
		notifyWindowFocused: true,
		initialSessionID:    initialSessionID,
		continueLastSession: continueLast,
		skillStates:         skills.GetLatestStates(),
	}

	// Cache active subagents once — they are static for the session.
	ui.activeSubagentItems, ui.activeSubagentNames = buildSubagentCaches(com.Workspace.ActiveSubagents())

	status := NewStatus(com, ui)

	// Seed the active theme key from the large model provider so the
	// first model selection can correctly skip a redundant theme swap.
	// A configured theme (options.tui.theme) pins the theme for the
	// whole session; the key carries a "config:" prefix so provider
	// switches can never match it.
	if cfg := com.Config(); cfg != nil {
		if name := common.ThemeNameFromConfig(cfg); name != "" {
			ui.themeKey = "config:" + strings.ToLower(name)
		} else {
			ui.themeKey = styles.ThemeKeyForProvider(cfg.Models[config.SelectedModelTypeLarge].Provider)
		}
	}

	// Seed the memoized agent ready/model state the same way so the first
	// frame renders the model info; the busy probe keeps it fresh
	// afterwards.
	if com.Workspace.AgentIsReady() {
		ui.agentReady = true
		ui.agentModel = com.Workspace.AgentModel()
	}
	ui.setEditorPrompt()
	ui.randomizePlaceholders()
	ui.textarea.Placeholder = ui.readyPlaceholder
	ui.status = status

	// Initialize compact mode from config

	desiredState := uiLanding
	desiredFocus := uiFocusEditor
	if !com.Config().IsConfigured() {
		desiredState = uiOnboarding
	}

	// set initial state
	ui.setState(desiredState, desiredFocus)

	opts := com.Config().Options

	// Suppress thinking-block rendering in the transcript when
	// configured. Set before any message items are built; reasoning is
	// still requested, streamed and stored.
	chat.HideThinking = !opts.TUI.ShouldShowThinking()

	// disable indeterminate progress bar
	ui.progressBarEnabled = opts.Progress == nil || *opts.Progress
	// enable transparent mode
	ui.isTransparent = opts.TUI.IsTransparent()
	// enable mouse support (default on)
	ui.mouseEnabled = opts.TUI.Mouse == nil || *opts.TUI.Mouse

	return ui
}

// Init initializes the UI model.
func (m *UI) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.state == uiOnboarding {
		if cmd := m.openModelsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// load the user commands async
	cmds = append(cmds, m.loadCustomCommands())
	// load prompt history async
	cmds = append(cmds, m.loadPromptHistory())
	// Prime the memoized LSP state off-thread.
	if cmd := m.requestLSPRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// load initial session if specified
	if cmd := m.loadInitialSession(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// Prime the memoized busy/permission state off-thread.
	if cmd := m.dispatchBusyRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// Subscribe to git watcher changes so the header repaints itself
	// when a background refresh produces a different summary.
	cmds = append(cmds, waitGitStatusChanged)
	cmds = append(cmds, m.checkPendingMCPAuth())
	return tea.Batch(cmds...)
}

// loadInitialSession loads the initial session if one was specified on startup.
func (m *UI) loadInitialSession() tea.Cmd {
	switch {
	case m.state != uiLanding:
		// Only load if we're in landing state (i.e., fully configured)
		return nil
	case m.initialSessionID != "":
		return m.loadSession(m.initialSessionID)
	case m.continueLastSession:
		return func() tea.Msg {
			sessions, err := m.com.Workspace.ListSessions(context.Background())
			if err != nil || len(sessions) == 0 {
				return nil
			}
			return m.loadSession(sessions[0].ID)()
		}
	default:
		return nil
	}
}

// sendNotification returns a command that sends a notification if allowed by policy.
func (m *UI) sendNotification(n notification.Notification) tea.Cmd {
	if !m.shouldSendNotification() {
		return nil
	}

	return m.notifyBackend.Send(n)
}

// selectNotificationBackend chooses the appropriate notification backend based
// on terminal capabilities, environment, and user configuration. This is a pure
// function that should be called once during initialization or when capabilities
// change.
func selectNotificationBackend(caps common.Capabilities, cfg *config.Config) notification.Backend {
	// Check for explicit user preference first.
	if cfg != nil && cfg.Options != nil && cfg.Options.Notifications != "" {
		switch cfg.Options.Notifications {
		case "native":
			if !notification.NativeSupported {
				slog.Debug("Native notifications unavailable on this platform; using OSC backend", "osc99_supported", caps.OSC99Notifications)
				return notification.NewOSCBackend(notification.Icon, caps.OSC99Notifications)
			}
			slog.Debug("Using native backend (user preference)")
			return notification.NewNativeBackend(notification.Icon)
		case "osc":
			slog.Debug("Using OSC backend (user preference)", "osc99_supported", caps.OSC99Notifications)
			return notification.NewOSCBackend(notification.Icon, caps.OSC99Notifications)
		case "bell":
			slog.Debug("Using bell backend (user preference)")
			return notification.NewBellBackend()
		case "disabled":
			slog.Debug("Notifications disabled (user preference)")
			return notification.NoopBackend{}
		case "auto":
			// Fall through to auto-detection below.
		default:
			slog.Warn("Unknown notification style, using auto", "style", cfg.Options.Notifications)
		}
	}

	// Auto-detect based on environment and capabilities.
	_, isSSH := caps.Env.LookupEnv("SSH_TTY")

	// SSH sessions use terminal-based notifications (OSC 99 or 777).
	if isSSH {
		slog.Debug("Selected OSCBackend for SSH session", "osc99_supported", caps.OSC99Notifications)
		return notification.NewOSCBackend(notification.Icon, caps.OSC99Notifications)
	}

	// Local sessions: prefer OSC on macOS because the native backend (beeep)
	// uses terminal-notifier or AppleScript, which is slow and doesn't display
	// icons properly. Also prefer OSC where native notifications are unavailable
	// (illumos/solaris). OSC 99 provides a polished experience with icon support.
	if runtime.GOOS == "darwin" || !notification.NativeSupported {
		slog.Debug("Selected OSCBackend for local session", "osc99_supported", caps.OSC99Notifications, "native_supported", notification.NativeSupported)
		return notification.NewOSCBackend(notification.Icon, caps.OSC99Notifications)
	}

	// Non-macOS local sessions use native OS notifications if focus events are supported.
	// Without focus events, we can't suppress notifications when focused, so
	// we disable them entirely to avoid spamming the user.
	if caps.ReportFocusEvents {
		slog.Debug("Selected NativeBackend for local session")
		return notification.NewNativeBackend(notification.Icon)
	}

	slog.Debug("Selected NoopBackend (focus events not supported)")
	return notification.NoopBackend{}
}

func (m *UI) updateNotificationBackend() {
	cfg := m.com.Config()
	m.notifyBackend = selectNotificationBackend(m.caps, cfg)
}

// shouldSendNotification returns true if notifications should be sent based on
// current state. Focus reporting must be supported, window must not be
// focused, and notifications must not be disabled in config.
func (m *UI) shouldSendNotification() bool {
	cfg := m.com.Config()
	if cfg != nil && cfg.Options != nil && cfg.Options.Notifications == "disabled" {
		return false
	}
	return m.caps.ReportFocusEvents && !m.notifyWindowFocused
}

// setState changes the UI state and focus.
func (m *UI) setState(state uiState, focus uiFocusState) {
	m.state = state
	m.focus = focus
	// Changing the state may change layout, so update it.
	m.updateLayoutAndSize()
}

// focusEditor moves keyboard focus to the editor, textarea or inline
// form, and returns the command that restarts the caret's blink
// cycle. textarea.Focus produces that command and dropping it freezes
// the caret in whatever blink phase it was in, including invisible,
// until the next refocus, so every path into the editor flows through
// here and threads the command back to Update.
func (m *UI) focusEditor() tea.Cmd {
	m.focus = uiFocusEditor
	m.chat.Blur()
	if m.activeInline != nil {
		m.textarea.Blur()
		m.activeInline.SetFocused(true)
		return nil
	}
	return m.textarea.Focus()
}

// loadCustomCommands loads the custom commands asynchronously.
func (m *UI) loadCustomCommands() tea.Cmd {
	return func() tea.Msg {
		customCommands, err := commands.LoadCustomCommands(m.com.Config())
		if err != nil {
			slog.Error("Failed to load custom commands", "error", err)
		}
		// Append user-invocable skills as commands.
		skillEntries, err := m.com.Workspace.ListSkills(context.Background())
		if err != nil {
			slog.Error("Failed to load skill commands", "error", err)
		}
		customCommands = append(customCommands, commands.FromSkillCatalog(skillEntries)...)
		// Commands registered by Lua extensions. Their content is
		// produced by the extension when the command runs.
		extensionCommands, err := m.com.Workspace.ListExtensionCommands(context.Background())
		if err != nil {
			slog.Error("Failed to load extension commands", "error", err)
		}
		customCommands = append(customCommands, commands.FromExtensions(extensionCommands)...)
		return userCommandsLoadedMsg{Commands: customCommands}
	}
}

// applyChatScroll scrolls the chat by lines and, if the selection is then
// outside the viewport, moves it to the nearest visible edge. The selection
// is moved rather than scrolled to so a large coalesced delta is applied in
// full instead of being rewound to the selected item.
func (m *UI) applyChatScroll(lines int) {
	m.chat.ScrollBy(lines)
	if m.chat.SelectedItemInView() {
		return
	}
	if lines > 0 && m.chat.AtBottom() {
		m.chat.SelectLast()
		return
	}
	m.chat.SelectNearestInView(lines < 0)
}

// loadMCPrompts loads the MCP prompts asynchronously.
func (m *UI) loadMCPrompts() tea.Msg {
	prompts, err := m.com.Workspace.ListMCPPrompts(context.Background())
	if err != nil {
		slog.Error("Failed to load MCP prompts", "error", err)
	}
	if prompts == nil {
		// flag them as loaded even if there is none or an error
		prompts = []commands.MCPPrompt{}
	}
	return mcpPromptsLoadedMsg{Prompts: prompts}
}

// Update handles updates to the UI model.
func (m *UI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	m.beginFrameUpdate()
	// Update terminal capabilities
	m.caps.Update(msg)
	switch msg := msg.(type) {
	case tea.EnvMsg:
		// Is this Windows Terminal?
		if !m.sendProgressBar {
			m.sendProgressBar = slices.Contains(msg, "WT_SESSION")
		}
		cmds = append(cmds, common.QueryCmd(uv.Environ(msg)))
	case tea.ModeReportMsg:
		m.updateNotificationBackend()
	case uv.UnknownOscEvent:
		m.updateNotificationBackend()
	case tea.FocusMsg:
		m.notifyWindowFocused = true
	case tea.BlurMsg:
		m.notifyWindowFocused = false
	case gitStatusChangedMsg:
		// A background git refresh produced a different summary;
		// re-arm the subscription and let the repaint read the cache.
		cmds = append(cmds, waitGitStatusChanged)
	case pubsub.Event[notify.Notification]:
		if cmd := m.handleAgentNotification(msg.Payload); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case busyStateMsg:
		cmds = append(cmds, m.applyBusyState(msg)...)
	case promptQueueMsg:
		cmds = append(cmds, m.applyPromptQueue(msg)...)
	case lspStatesMsg:
		if servers, ok := m.dialog.Dialog(dialog.LSPServersID).(*dialog.LSPServers); ok {
			servers.SetStates(msg.states, msg.diagnostics)
		}
		if cmd := m.applyLSPStates(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case agentModelChangedMsg:
		// The coordinator model changed (selection, thinking, reasoning):
		// re-fetch the memoized ready/model state off-thread.
		m.invalidateBusyCaches()
		if cmd := m.dispatchBusyRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case agentRunSubmittedMsg:
		// A prompt was just accepted (run started or enqueued): fetch the
		// authoritative busy/queue state to confirm the optimistic values
		// sendMessage wrote.
		m.invalidateBusyCaches()
		m.invalidatePromptQueue()
		if cmd := m.dispatchBusyRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case loadSessionMsg:
		// A session switch leaves any retry notice behind: it
		// belonged to the previous session's backoff.
		m.clearRetryNotice()
		m.setState(uiChat, m.focus)
		m.session = msg.session
		m.sessionFiles = msg.files
		// Session switch: the memoized busy state and queued prompts
		// belong to the previous session. Drop them and re-fetch
		// off-thread so the queue pill and esc behavior track the new
		// session instead of a stale one.
		m.invalidateBusyCaches()
		m.invalidatePromptQueue()
		m.promptQueue = 0
		m.promptQueueItems = nil
		m.promptQueueCheckedAt = time.Time{}
		if cmd := m.dispatchBusyRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.parentTitle = ""
		m.subagentColor = ""
		m.knownChildSessionIDs = nil
		// runningSubagents is otherwise only refreshed by a live RuntimeEvent
		// for the current session's parent — drop the previous session's
		// list and re-fetch for the new one so the sidebar doesn't keep
		// showing a stale "Active subagents" panel until one happens to
		// arrive (or never, if nothing is dispatched under the new session).
		m.runningSubagents = nil
		m.resetAgentTasks()
		m.pendingModelAction = nil
		cmds = append(cmds, m.refreshRunningSubagents(m.session.ID))
		cmds = append(cmds, m.startLSPs(msg.lspFilePaths()))
		msgs, err := m.com.Workspace.ListMessages(context.Background(), m.session.ID)
		if err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}
		if cmd := m.setSessionMessages(msgs); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.restoreModelFromSession(msgs); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.autoExpandPillsIfReasonable(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		// If a bang command was issued before the session finished
		// loading, start it now that the chat list is stable.
		if m.pendingBangCommand != "" {
			cmds = append(cmds, m.runShellCommandInternal(m.pendingBangCommand, true))
			m.pendingBangCommand = ""
		}
		if hasInProgressTodo(m.session.Todos) {
			// only start spinner if there is an in-progress todo
			if m.isAgentBusy() {
				m.todoIsSpinning = true
				cmds = append(cmds, m.todoSpinner.Tick)
			}
			m.updateLayoutAndSize()
		}
		// Reload prompt history for the new session.
		m.historyReset()
		cmds = append(cmds, m.loadPromptHistory())
		if m.session.ParentSessionID != "" {
			cmds = append(cmds, m.fetchParentMeta(m.session.ParentSessionID, m.session.ID))
		}
		m.updateLayoutAndSize()

	case parentTitleMsg:
		// Discard a fetch that raced a session switch: the user navigated
		// away from forSession before it resolved, so applying it here would
		// show a breadcrumb that belongs to a different (departed) session.
		if msg.forSession == m.currentSessionID() {
			m.parentTitle = msg.title
			m.subagentColor = msg.color
		}

	case sessionFilesUpdatesMsg:
		m.sessionFiles = msg.sessionFiles
		var paths []string
		for _, f := range msg.sessionFiles {
			paths = append(paths, f.LatestVersion.Path)
		}
		cmds = append(cmds, m.startLSPs(paths))

	case sendMessageMsg:
		cmds = append(cmds, m.sendMessage(msg.Content, msg.Attachments...))

	case extensionCommandExpandedMsg:
		cmds = append(cmds, m.sendMessage(msg.Prompt))
	case userCommandsLoadedMsg:
		m.customCommands = msg.Commands
		dia := m.dialog.Dialog(dialog.CommandsID)
		if dia == nil {
			break
		}

		commands, ok := dia.(*dialog.Commands)
		if ok {
			commands.SetCustomCommands(m.customCommands)
		}

	case mcpStateChangedMsg:
		m.mcpStates = msg.states
		if servers, ok := m.dialog.Dialog(dialog.MCPServersID).(*dialog.MCPServers); ok {
			servers.SetStates(msg.states)
		}
		// Auto-open the MCP auth dialog if any servers need authentication.
		if cmd := m.openMCPAuthDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case mcpPromptsLoadedMsg:
		m.mcpPrompts = msg.Prompts
		dia := m.dialog.Dialog(dialog.CommandsID)
		if dia == nil {
			break
		}

		commands, ok := dia.(*dialog.Commands)
		if ok {
			commands.SetMCPPrompts(m.mcpPrompts)
		}

	case promptHistoryLoadedMsg:
		m.promptHistory.messages = msg.messages
		m.promptHistory.index = -1
		m.promptHistory.draft = ""

	case closeDialogMsg:
		m.dialog.CloseFrontDialog()

	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.DeletedEvent {
			if m.session != nil && m.session.ID == msg.Payload.ID {
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			break
		}
		if m.session != nil && msg.Payload.ID == m.session.ID {
			prevHasInProgress := hasInProgressTodo(m.session.Todos)
			prevPillsHeight := m.pillsAreaHeight()
			m.session = &msg.Payload
			if !prevHasInProgress && hasInProgressTodo(m.session.Todos) {
				m.todoIsSpinning = true
				cmds = append(cmds, m.todoSpinner.Tick)
			}
			// The pills panel reserves vertical space that the chat area
			// must yield. Recompute the layout whenever that footprint
			// changes (todos appearing, the list growing, etc.) so the
			// box renders on first paint rather than waiting for a toggle.
			// When the footprint is unchanged we still re-render the pill
			// content so status changes (e.g. the in-progress spinner)
			// show up.
			if m.pillsAreaHeight() != prevPillsHeight {
				m.updateLayoutAndSize()
			} else {
				m.renderPills()
			}
			m.autoExpandPillsIfReasonable()
		}
	case pubsub.Event[message.Message]:
		// Any tool result may have written to the tree - edit, write,
		// bash, even an MCP tool - so ask the git segment to re-check
		// itself. The poke is debounced into at most one git invocation
		// per gitWatchDebounce window.
		if hasToolResult(msg.Payload) {
			gitWatch.pokeSoon()
		}
		// Check if this is a child session message for an agent tool.
		if m.session == nil {
			break
		}
		if msg.Payload.SessionID != m.session.ID {
			// This might be a child session message from an agent tool.
			if cmd := m.handleChildSessionMessage(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
			break
		}
		switch msg.Type {
		case pubsub.CreatedEvent:
			cmds = append(cmds, m.appendSessionMessage(msg.Payload))
			// A new message is a run boundary — a user prompt starting
			// a turn or the agent replying/dequeueing. Drop the
			// memoized busy state and re-fetch it and the queue
			// off-thread. Per-chunk UpdatedEvents deliberately do NOT
			// trigger this: during streaming that would put workspace
			// probes on every token.
			m.invalidateBusyCaches()
			m.invalidatePromptQueue()
			if cmd := m.dispatchBusyRefresh(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		case pubsub.UpdatedEvent:
			cmds = append(cmds, m.updateSessionMessage(msg.Payload))
		case pubsub.DeletedEvent:
			m.chat.RemoveMessage(msg.Payload.ID)
		}
		// Any message traffic on the current session means the turn
		// moved past the backoff: drop a lingering retry notice.
		m.clearRetryNotice()
		// start the spinner if there is a new message
		if hasInProgressTodo(m.session.Todos) && m.isAgentBusy() && !m.todoIsSpinning {
			m.todoIsSpinning = true
			cmds = append(cmds, m.todoSpinner.Tick)
		}
		// stop the spinner if the agent is not busy anymore
		if m.todoIsSpinning && !m.isAgentBusy() {
			m.todoIsSpinning = false
		}
		// there is a number of things that could change the pills here so we want to re-render
		m.renderPills()
	case pubsub.Event[history.File]:
		cmds = append(cmds, m.handleFileEvent(msg.Payload))
	case pubsub.Event[app.LSPEvent]:
		// Refresh the memoized LSP state off-thread: LSPGetStates is a
		// synchronous HTTP round-trip in client/server mode and diagnostics
		// events can arrive per edited file.
		if cmd := m.requestLSPRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pubsub.Event[workspace.LSPEvent]:
		if cmd := m.requestLSPRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pubsub.Event[skills.Event]:
		m.skillStates = msg.Payload.States
	case pubsub.Event[subagents.RuntimeEvent]:
		switch {
		case m.session == nil:
			m.runningSubagents = nil
			m.knownChildSessionIDs = nil
		case msg.Payload.ParentSessionID == m.session.ID:
			// Only the current session's children populate the strip; ignore
			// events for other parents to avoid spurious DB refreshes.
			cmds = append(cmds, m.refreshRunningSubagents(m.session.ID))
			if m.knownChildSessionIDs == nil {
				m.knownChildSessionIDs = make(map[string]bool)
			}
			for _, e := range msg.Payload.Entries {
				m.knownChildSessionIDs[e.ChildSessionID] = true
				m.applyRunningSubagentInfo(childSessionInfo{
					ChildSessionID: e.ChildSessionID,
					Name:           e.Name,
					Color:          e.Color,
					Model:          e.Model,
					Status:         e.Status,
				})
			}
			if f := msg.Payload.Finished; f != nil {
				m.knownChildSessionIDs[f.ChildSessionID] = true
				m.applyRunningSubagentInfo(childSessionInfo{
					ChildSessionID: f.ChildSessionID,
					Name:           f.Name,
					Color:          f.Color,
					Status:         f.Status,
				})
			}
		}
		m.updateLayoutAndSize()
		if m.tasksSpinning() {
			cmds = append(cmds, m.taskSpinner.Tick)
		}
		if f := msg.Payload.Finished; f != nil && m.session != nil && f.ParentSessionID == m.session.ID {
			cmds = append(cmds, util.ReportInfo(fmt.Sprintf("Subagent %s %s", f.Name, f.Status)))
		}
		if m.dialog.HasDialogs() {
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	case runningSubagentsMsg:
		// Discard a fetch that raced a session switch: the user navigated
		// away from forSession before it resolved, so applying it here would
		// clobber the newly-loaded session's (possibly already-refreshed)
		// list with a stale one.
		if msg.forSession == m.currentSessionID() {
			m.runningSubagents = msg.list
			for _, info := range msg.list {
				m.applyRunningSubagentInfo(childSessionInfo{
					ChildSessionID:   info.ChildSessionID,
					Name:             info.Name,
					Color:            info.Color,
					Model:            info.Model,
					Status:           info.Status,
					PromptTokens:     info.PromptTokens,
					CompletionTokens: info.CompletionTokens,
				})
			}
			// Seed the child-session set from the fetch as well. On a session
			// switch loadSessionMsg clears knownChildSessionIDs, and the only
			// other place it is filled is the RuntimeEvent case — so a subagent
			// that was already running before the switch would have its
			// history.File events rejected by handleFileEvent until it next
			// published an event, losing its edits from the Modified Files
			// panel in the meantime.
			for _, info := range msg.list {
				if m.knownChildSessionIDs == nil {
					m.knownChildSessionIDs = make(map[string]bool)
				}
				m.knownChildSessionIDs[info.ChildSessionID] = true
			}
			// The fetched list is authoritative for what is actually
			// still running: settle background dispatches whose Finished
			// event was missed (e.g. raced a session switch).
			m.reconcileBackgroundTasks(msg.list)
		}
	case pubsub.Event[subagents.Event]:
		// Library discovery changed (e.g. a delete) — rebuild the @-mention
		// caches so removed subagents stop being offered without a restart.
		m.rebuildSubagentCaches()
	case pubsub.Event[mcp.Event]:
		switch msg.Payload.Type {
		case mcp.EventStateChanged:
			return m, tea.Batch(
				m.handleStateChanged(),
				m.loadMCPrompts,
			)
		case mcp.EventPromptsListChanged:
			return m, handleMCPPromptsEvent(m.com.Workspace, msg.Payload.Name)
		case mcp.EventToolsListChanged:
			return m, handleMCPToolsEvent(m.com.Workspace, msg.Payload.Name)
		case mcp.EventResourcesListChanged:
			return m, handleMCPResourcesEvent(m.com.Workspace, msg.Payload.Name)
		}
	case pubsub.Event[question.Request]:
		m.openBatchFormDialog(msg.Payload)
		m.chat.ScrollToBottom()
		if cmd := m.sendNotification(notification.Notification{
			Title:   "Agent is waiting...",
			Message: fmt.Sprintf("%d questions need your input", len(msg.Payload.Questions)),
		}); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pubsub.Event[question.Notification]:
		cmds = append(cmds, m.handleQuestionNotification(msg.Payload))
	case cancelTimerExpiredMsg:
		m.isCanceling = false
		m.rewindEscArmed = false
	case quitTimerExpiredMsg:
		m.isQuitting = false
	case tea.TerminalVersionMsg:
		termVersion := strings.ToLower(msg.Name)
		// Only enable progress bar for the following terminals.
		if !m.sendProgressBar {
			m.sendProgressBar = xstrings.ContainsAnyOf(termVersion, "ghostty", "iterm2", "rio")
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Suppress the chat's full-height scan during the resize so a drag
		// only reflows visible items; it settles (and recomputes) shortly
		// after the last resize event.
		if m.state == uiChat {
			cmds = append(cmds, m.chat.BeginResize())
		}
		m.updateLayoutAndSize()
		if m.state == uiChat && m.chat.Follow() {
			m.chat.ScrollToBottom()
		}
	case tea.KeyboardEnhancementsMsg:
		m.keyenh = msg
		if msg.SupportsKeyDisambiguation() {
			// Prefer the disambiguated key in the help text, but only
			// while the binding still carries it — options.tui.keybinds
			// may have rebound the action elsewhere. The newline hint
			// always names shift+enter; no swap needed there.
			if slices.Contains(m.keyMap.Models.Keys(), "ctrl+m") {
				m.keyMap.Models.SetHelp("ctrl+m", "models")
			}
		}
	case copyChatHighlightMsg:
		cmds = append(cmds, m.copyChatHighlight())
	case DelayedClickMsg:
		// Handle delayed single-click action (e.g., expansion).
		m.chat.HandleDelayedClick(msg)
	case tea.MouseClickMsg:
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}

		// Route clicks to inline editors that support mouse interaction.
		if m.activeInline != nil {
			if clickable, ok := m.activeInline.(dialog.MouseClickableEditor); ok {
				if done, handled := clickable.HandleMouseClick(msg.X, msg.Y); handled {
					if done {
						m.activeInline = nil
						cmds = append(cmds, m.focusEditor())
						m.updateLayoutAndSize()
					}
					return m, tea.Batch(cmds...)
				}
			}
		}

		if cmd := m.handleClickFocus(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}

		// Check if the click landed on an attachment's remove button.
		// The attachment chips are rendered on the content's first row
		// (editorContentOrigin), above the textarea.
		if m.activeInline == nil && msg.Button == uv.MouseLeft && m.hasAttachments() && msg.Y == m.editorContentOrigin().Y {
			relX := msg.X - m.layout.editor.Min.X
			if m.attachments.HandleClick(relX) {
				return m, tea.Batch(cmds...)
			}
		}

		// Forward clicks within the textarea region to the textarea so it
		// can position the cursor and start a selection.
		if m.activeInline == nil {
			if handled, cmd := m.forwardMouseToTextarea(msg); handled {
				cmds = append(cmds, cmd)
				return m, tea.Batch(cmds...)
			}
		}

		switch m.state {
		case uiChat:
			// Clicks in the background tasks strip toggle its entries
			// before the chat sees them.
			if image.Pt(msg.X, msg.Y).In(m.layout.tasks) {
				if m.handleTaskClick(msg.X, msg.Y-m.layout.tasks.Min.Y) {
					return m, tea.Batch(cmds...)
				}
			}
			x, y := msg.X, msg.Y
			// Adjust for chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			if handled, cmd := m.chat.HandleMouseDown(x, y); handled {
				m.lastClickTime = time.Now()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}

	case tea.MouseMotionMsg:
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		// Track hover position for inline editors.
		if m.activeInline != nil {
			if m.hoverX != msg.X || m.hoverY != msg.Y {
				m.hoverX = msg.X
				m.hoverY = msg.Y
				if clickable, ok := m.activeInline.(dialog.MouseClickableEditor); ok {
					clickable.SetHover(msg.X, msg.Y)
				}
			}
		}

		// While a mouse selection is in progress in the textarea, forward
		// motion events to it and skip chat drag handling.
		if m.activeInline == nil && m.textareaMouseSelecting {
			if handled, cmd := m.forwardMouseToTextarea(msg); handled {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}

		switch m.state {
		case uiChat:
			// Skip chat edge-scrolling when an inline editor is
			// active to prevent accidental scrolling while hovering
			// over question forms or other inline components.
			if m.activeInline != nil && m.focus == uiFocusEditor {
				break
			}
			if msg.Y <= 0 {
				m.chat.ScrollBy(-1)
				if !m.chat.SelectedItemInView() {
					m.chat.SelectPrev()
					m.chat.ScrollToSelected()
				}
			} else if msg.Y >= m.chat.Height()-1 {
				m.chat.ScrollBy(1)
				if !m.chat.SelectedItemInView() {
					m.chat.SelectNext()
					m.chat.ScrollToSelected()
				}
			}

			x, y := msg.X, msg.Y
			// Adjust for chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			m.chat.HandleMouseDrag(x, y)
		}

	case tea.MouseReleaseMsg:
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		// End any in-progress textarea mouse selection.
		if m.textareaMouseSelecting {
			m.textareaMouseSelecting = false
			if handled, cmd := m.forwardMouseToTextarea(msg); handled {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}

		switch m.state {
		case uiChat:
			x, y := msg.X, msg.Y
			// Adjust for chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			if m.chat.HandleMouseUp(x, y) && m.chat.HasHighlight() {
				cmds = append(cmds, tea.Tick(doubleClickThreshold, func(t time.Time) tea.Msg {
					if time.Since(m.lastClickTime) >= doubleClickThreshold {
						return copyChatHighlightMsg{}
					}
					return nil
				}))
			}
		}
	case common.CoalescedWheelMsg:
		// Route wheel events to active inline editor only when the
		// mouse is over the editor area, so scrolling over the chat
		// still scrolls the chat.
		if m.activeInline != nil && image.Pt(msg.Mouse.X, msg.Mouse.Y).In(m.layout.editor) {
			if we, ok := m.activeInline.(common.WheelScrollable); ok {
				we.HandleWheel(msg.DeltaX, msg.DeltaY)
				return m, tea.Batch(cmds...)
			}
		}

		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		// Otherwise handle mouse wheel for chat. Use the coalesced delta
		// directly as the line count. Terminals like Ghostty send DeltaY=3
		// per physical wheel tick (matching their native scrollback), while
		// others send DeltaY=1.
		switch m.state {
		case uiChat:
			if msg.DeltaX != 0 {
				m.chat.ScrollSelectedShellHorizontal(int(msg.DeltaX))
			}
			lines := int(msg.DeltaY)
			if lines == 0 {
				break
			}
			m.markScrollOnly()
			m.applyChatScroll(lines)
		}
	case frameGCMsg:
		m.handleFrameGC()
	case animTickMsg:
		if cmd := m.handleAnimTick(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case scrollbarHideMsg:
		if m.state == uiChat {
			m.chat.HideScrollbar(msg.seq)
		}
	case chatWarmMsg:
		// A resize has settled; warm the message cache one batch at a time
		// so the scrollbar recompute never blocks the UI thread.
		if m.state == uiChat {
			cmd, done := m.chat.WarmStep(msg.seq)
			if cmd != nil {
				cmds = append(cmds, cmd)
			} else if done {
				// Heights are cached now, so the final layout pass (scrollbar
				// reservation) is cheap.
				m.updateLayoutAndSize()
			}
		}
	case spinner.TickMsg:
		if m.dialog.HasDialogs() {
			// route to dialog
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.state == uiChat && m.hasSession() && hasInProgressTodo(m.session.Todos) && m.todoIsSpinning {
			var cmd tea.Cmd
			m.todoSpinner, cmd = m.todoSpinner.Update(msg)
			if cmd != nil {
				m.renderPills()
				cmds = append(cmds, cmd)
			}
		}
		if m.state == uiChat && m.tasksSpinning() {
			var cmd tea.Cmd
			m.taskSpinner, cmd = m.taskSpinner.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			// Backstop: settle tasks whose terminal RuntimeEvent was lost
			// in flight, so no spinner outlives its subagent.
			if cmd := m.maybeReconcileSpinningTasks(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

	case tea.KeyPressMsg:
		if cmd := m.handleKeyPressMsg(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case tea.PasteMsg:
		if m.activeInline != nil && m.focus == uiFocusEditor {
			if p, ok := m.activeInline.(dialog.PasteableEditor); ok {
				if cmd := p.HandlePaste(msg); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return m, tea.Batch(cmds...)
			}
		}
		if cmd := m.handlePasteMsg(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pastedAttachmentMsg:
		// A paste landed: reference it inline in the editor with a
		// placeholder token instead of adding a pill to the attachments
		// strip (see paste.go).
		m.insertPastedAttachment(msg)
	case openEditorMsg:
		prevHeight := m.textarea.Height()
		m.textarea.SetValue(msg.Text)
		m.textarea.MoveToEnd()
		m.syncBangModeFromTextarea()
		cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))
	case shellStreamMsg:
		if item := m.chat.MessageItem(msg.PendingID); item != nil {
			if shellItem, ok := item.(*chat.ShellItem); ok {
				shellItem.AppendOutput(msg.Chunk)
				m.chat.ScrollToBottom()
			}
		}
		// Continue draining the stream channel.
		if msg.streamCh != nil {
			ch := msg.streamCh
			pid := msg.PendingID
			cmds = append(cmds, func() tea.Msg {
				chunk, ok := <-ch
				if !ok {
					return nil
				}
				return shellStreamMsg{PendingID: pid, Chunk: chunk, streamCh: ch}
			})
		}
	case shellResultMsg:
		// Clear the bang cancel func — command is done.
		if m.bangCancel != nil {
			m.bangCancel()
			m.bangCancel = nil
		}
		// Complete the pending shell item if it exists, otherwise create a new one.
		completed := false
		if msg.PendingID != "" {
			if item := m.chat.MessageItem(msg.PendingID); item != nil {
				if shellItem, ok := item.(*chat.ShellItem); ok {
					shellItem.Complete(msg.Output, msg.ExitCode)
					m.chat.ScrollToBottom()
					completed = true
				}
			}
		}
		if !completed {
			item := chat.NewShellItem(m.com.Styles, msg.Command, msg.Output, msg.ExitCode)
			m.chat.AppendMessages(item)
			m.chat.ScrollToBottom()
		}
		cmds = append(cmds, m.loadPromptHistory())
	case util.InfoMsg:
		if msg.Type == util.InfoTypeError {
			slog.Error("Error reported", "error", msg.Msg)
		}
		m.status.SetInfoMsg(msg)
		ttl := msg.TTL
		if ttl <= 0 {
			ttl = DefaultStatusTTL
		}
		cmds = append(cmds, clearInfoMsgCmd(ttl))
	case app.UpdateAvailableMsg:
		text := fmt.Sprintf("Harness update available: v%s → v%s.", msg.CurrentVersion, msg.LatestVersion)
		if msg.IsDevelopment {
			text = fmt.Sprintf("This is a development version of Harness. The latest version is v%s.", msg.LatestVersion)
		}
		ttl := 10 * time.Second
		m.status.SetInfoMsg(util.InfoMsg{
			Type: util.InfoTypeUpdate,
			Msg:  text,
			TTL:  ttl,
		})
		cmds = append(cmds, clearInfoMsgCmd(ttl))
	case workspace.ConnectionEvent:
		cmds = append(cmds, m.handleConnectionEvent(msg)...)
	case util.ClearStatusMsg:
		m.status.ClearInfoMsg()
	case completions.CompletionItemsLoadedMsg:
		if m.completionsOpen {
			m.completions.SetItems(msg.Files, msg.Resources, msg.Subagents)
		}
	case uv.KittyGraphicsEvent:
		if !bytes.HasPrefix(msg.Payload, []byte("OK")) {
			slog.Warn("Unexpected Kitty graphics response",
				"response", string(msg.Payload),
				"options", msg.Options)
		}
	case dialog.ActionMCPAuthStarted:
		cmds = append(cmds, m.authenticateMCP(msg.Ctx, msg.Name))
	case dialog.ActionMCPAuthComplete, dialog.ActionMCPAuthErrored:
		if m.dialog.HasDialogs() {
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	default:
		if m.dialog.HasDialogs() {
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	// This logic gets triggered on any message type, but should it?
	prevPlaceholder := m.textarea.Placeholder
	switch m.focus {
	case uiFocusMain:
	case uiFocusEditor:
		// Textarea placeholder logic
		if m.bangMode {
			m.textarea.Placeholder = "Run a shell command"
		} else if m.isAgentBusy() {
			m.textarea.Placeholder = m.workingPlaceholder
		} else {
			m.textarea.Placeholder = m.readyPlaceholder
		}
	}
	if m.textarea.Placeholder != prevPlaceholder {
		m.invalidateFrames()
	}

	// TTL backstop: schedule an off-thread re-probe for any memoized
	// workspace state that has gone stale. Never does IO on this
	// goroutine.
	cmds = append(cmds, m.staleWorkspaceRefreshCmds()...)

	// at this point this can only handle [message.Attachment] message, and we
	// should return all cmds anyway.
	if m.attachments.Update(msg) {
		m.invalidateFrames()
		// The editor reserves a row for the attachments strip, so a
		// count change is a layout change.
		m.updateLayoutAndSize()
	}
	// Any update may have put a spinner on screen (new message, tool update,
	// scroll, session load); make sure the clock is running. This is the
	// sole place the clock is armed so a tick never sits inside a caller's
	// tea.Sequence.
	if m.state == uiChat {
		if cmd := m.chat.EnsureAnimating(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if cmd := m.endFrameUpdate(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// handleAnimTick advances every visible spinner by one frame. A tick that
// changed nothing visible is scroll-only so the frame cache survives; one
// that did keeps the view pinned to the bottom while following, since
// animated items can change height.
func (m *UI) handleAnimTick(msg animTickMsg) tea.Cmd {
	if m.state != uiChat {
		m.chat.stopAnimating(msg)
		m.markScrollOnly()
		return nil
	}
	changed, cmd := m.chat.Tick(msg)
	if !changed {
		m.markScrollOnly()
		return cmd
	}
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
	}
	return cmd
}

// setSessionMessages sets the messages for the current session in the chat
func (m *UI) setSessionMessages(msgs []message.Message) tea.Cmd {
	var cmds []tea.Cmd
	// Build tool result map to link tool calls with their results
	msgPtrs := make([]*message.Message, len(msgs))
	for i := range msgs {
		msgPtrs[i] = &msgs[i]
	}
	toolResultMap := chat.BuildToolResultMap(msgPtrs)
	if len(msgPtrs) > 0 {
		m.lastUserMessageTime = msgPtrs[0].CreatedAt
	}

	// Add messages to chat with linked tool results
	items := make([]chat.MessageItem, 0, len(msgs)*2)
	for _, msg := range msgPtrs {
		switch msg.Role {
		case message.User:
			m.lastUserMessageTime = msg.CreatedAt
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap, m.com.Workspace.WorkingDir())...)
		case message.Assistant:
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap, m.com.Workspace.WorkingDir())...)
			if chat.ShouldShowAssistantInfo(msg) {
				infoItem := chat.NewAssistantInfoItem(m.com.Styles, msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
				items = append(items, infoItem)
			}
		default:
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap, m.com.Workspace.WorkingDir())...)
		}
	}

	// Rebuild the background task list (subagents) from the same
	// messages; they do not render in the transcript.
	m.loadAgentTasks(msgPtrs, toolResultMap)

	// The rebuilt list drops the old session's queued-prompt
	// placeholders with it.
	m.resetQueuedPrompts()

	// If the user switches between sessions while the agent is working we
	// want to make sure the animations are shown. Gate on the agent actually
	// being busy: a session that was killed mid-generation can persist an
	// assistant message with no Finish part, which still reports Spinning()
	// even though nothing is running. Allowing the clock for it here would
	// leave a ghost "working" spinner (and a second one alongside any tool
	// spinner) after the session is reloaded. Messages arriving for the
	// session re-enable the clock.
	m.chat.SetAnimationsAllowed(m.isAgentBusy())

	if cmd := m.chat.SetMessages(items...); cmd != nil {
		cmds = append(cmds, cmd)
	}
	m.chat.SelectLast()
	return tea.Sequence(cmds...)
}

// handleConnectionEvent reports the health of the client-server link and,
// once it recovers, reloads the open session. A reload is always needed
// after a degraded episode: events published while the stream was down are
// gone, and if the workspace itself was re-created any run died with it.
func (m *UI) handleConnectionEvent(msg workspace.ConnectionEvent) []tea.Cmd {
	info := util.InfoMsg{
		Type: util.InfoTypeWarn,
		Msg:  "Lost connection to the Harness server — reconnecting…",
		TTL:  30 * time.Second,
	}
	switch msg.State {
	case workspace.ConnectionDegraded:
		slog.Warn("Server connection degraded", "error", msg.Err, "stuck", msg.Stuck)
		if msg.Stuck {
			info.Type = util.InfoTypeError
			info.Msg = "Can't restore the connection to the Harness server. Restart Harness to recover."
			info.TTL = time.Minute
		}
	case workspace.ConnectionRecovered:
		info = util.InfoMsg{
			Type: util.InfoTypeSuccess,
			Msg:  "Reconnected to the Harness server.",
			TTL:  DefaultStatusTTL,
		}
	}
	m.status.SetInfoMsg(info)
	cmds := []tea.Cmd{clearInfoMsgCmd(info.TTL)}
	if msg.State == workspace.ConnectionRecovered && m.session != nil {
		cmds = append(cmds, m.loadSession(m.session.ID))
	}
	return cmds
}

// appendSessionMessage appends a new message to the current session in the chat
// if the message is a tool result it will update the corresponding tool call message
func (m *UI) appendSessionMessage(msg message.Message) tea.Cmd {
	var cmds []tea.Cmd

	existing := m.chat.MessageItem(msg.ID)
	if existing != nil {
		// message already exists, skip
		return nil
	}

	switch msg.Role {
	case message.User:
		// A background sub-agent's report-back bumps its task strip
		// entry instead of rendering in the transcript.
		if msg.SubagentNotesOnly() {
			for _, note := range msg.SubagentNotes() {
				m.countSubagentNote(note)
			}
			m.updateLayoutAndSize()
			return nil
		}
		// A harness context note is history for the model only.
		if msg.ContextNotesOnly() {
			return nil
		}
		// Shell commands are rendered live via shellResultMsg; skip
		// the persisted duplicate.
		if message.HasPart[message.ShellCommand](&msg) {
			return nil
		}
		m.lastUserMessageTime = msg.CreatedAt
		items := chat.ExtractMessageItems(m.com.Styles, &msg, nil, m.com.Workspace.WorkingDir())
		m.chat.AppendMessages(items...)
		m.chat.ScrollToBottom()
		// A queued prompt became a real message: drop its placeholder in
		// the same pass so the swap is invisible.
		m.materializeQueuedPrompt(msg.Content().Text)
	case message.Assistant:
		items := chat.ExtractMessageItems(m.com.Styles, &msg, nil, m.com.Workspace.WorkingDir())
		m.chat.AppendMessages(items...)
		if m.chat.Follow() {
			m.chat.ScrollToBottom()
		}
		if chat.ShouldShowAssistantInfo(&msg) {
			infoItem := chat.NewAssistantInfoItem(m.com.Styles, &msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
			m.chat.AppendMessages(infoItem)
			if m.chat.Follow() {
				m.chat.ScrollToBottom()
			}
		}
	case message.Tool:
		for _, tr := range msg.ToolResults() {
			// A subagent's final answer closes its strip entry.
			if m.resolveAgentTaskResult(tr) {
				m.updateLayoutAndSize()
				continue
			}
			m.chat.UpdateToolItem(tr.ToolCallID, func(toolItem chat.ToolMessageItem) {
				toolItem.SetResult(&tr)
			})
			if m.chat.Follow() {
				m.chat.ScrollToBottom()
			}
		}
	}
	return tea.Sequence(cmds...)
}

func (m *UI) handleClickFocus(msg tea.MouseClickMsg) (cmd tea.Cmd) {
	switch {
	case m.state != uiChat:
		return nil
	case m.focus != uiFocusTasks && len(m.agentTasks) > 0 && image.Pt(msg.X, msg.Y).In(m.layout.tasks):
		m.focusTasks()
		m.handleTaskClick(msg.X, msg.Y-m.layout.tasks.Min.Y)
		return nil
	case m.focus != uiFocusEditor && image.Pt(msg.X, msg.Y).In(m.layout.editor):
		cmd = m.focusEditor()
	case m.focus != uiFocusMain && image.Pt(msg.X, msg.Y).In(m.layout.main):
		m.focus = uiFocusMain
		m.textarea.Blur()
		m.chat.Focus()
	}
	return cmd
}

// updateSessionMessage updates an existing message in the current session in
// the chat when an assistant message is updated it may include updated tool
// calls as well that is why we need to handle creating/updating each tool call
// message too.
func (m *UI) updateSessionMessage(msg message.Message) tea.Cmd {
	// A message update means work is active; the animation clock may have
	// been frozen by a non-busy session reload (ghost-spinner guard).
	m.chat.SetAnimationsAllowed(true)
	var cmds []tea.Cmd
	existingItem := m.chat.MessageItem(msg.ID)

	if existingItem != nil {
		if assistantItem, ok := existingItem.(*chat.AssistantMessageItem); ok {
			assistantItem.SetMessage(&msg)
		}
	}

	shouldRenderAssistant := chat.ShouldRenderAssistantMessage(&msg)
	// A message whose item has nothing left to render is removed again:
	// an empty shell would sit in the transcript as an invisible slot.
	// The removal is not limited to tool-call messages — a message that
	// finishes without content is just as empty.
	if existingItem != nil && !shouldRenderAssistant {
		m.chat.RemoveMessage(msg.ID)
	}

	// The info item shows for every turn with a Prism-routed model, and
	// for the final turn of the prompt. It is removed again when the
	// turn no longer qualifies (e.g. a retry reset the stream).
	if infoItem := m.chat.MessageItem(chat.AssistantInfoID(msg.ID)); chat.ShouldShowAssistantInfo(&msg) {
		if infoItem == nil {
			newInfoItem := chat.NewAssistantInfoItem(m.com.Styles, &msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
			m.chat.AppendMessages(newInfoItem)
		}
	} else if infoItem != nil {
		m.chat.RemoveMessage(chat.AssistantInfoID(msg.ID))
	}

	var items []chat.MessageItem
	for _, tc := range msg.ToolCalls() {
		// Subagent dispatches live in the background tasks strip.
		if chat.IsSubagentTool(tc.Name) {
			if cmd := m.upsertAgentTask(&msg, tc); cmd != nil {
				cmds = append(cmds, cmd)
			}
			continue
		}
		// Context plumbing (skill_search, tool_search) stays in the
		// history the model sees but is noise in the transcript.
		if chat.IsInternalContextTool(tc.Name) {
			continue
		}
		if toolItem := m.chat.ToolItem(tc.ID); toolItem != nil {
			existingToolCall := toolItem.ToolCall()
			// only update if finished state changed or input changed
			// to avoid clearing the cache
			if (tc.Finished && !existingToolCall.Finished) || tc.Input != existingToolCall.Input {
				m.chat.UpdateToolItem(tc.ID, func(item chat.ToolMessageItem) {
					item.SetToolCall(tc)
				})
			}
			continue
		}
		items = append(items, chat.NewToolMessageItem(m.com.Styles, msg.ID, tc, nil, false, m.com.Workspace.WorkingDir()))
	}

	m.chat.AppendMessages(items...)
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
		if !m.chat.HasManualSelection() {
			m.chat.SelectLast()
		}
	}

	return tea.Sequence(cmds...)
}

// handleChildSessionMessage handles messages from child sessions
// (agent tools): their tool activity feeds the background tasks strip,
// not the transcript.
func (m *UI) handleChildSessionMessage(event pubsub.Event[message.Message]) tea.Cmd {
	// Only process messages with tool calls or results.
	if len(event.Payload.ToolCalls()) == 0 && len(event.Payload.ToolResults()) == 0 {
		return nil
	}

	// Check if this is an agent tool session and parse it.
	if _, _, ok := m.com.Workspace.ParseAgentToolSessionID(event.Payload.SessionID); !ok {
		return nil
	}

	m.updateAgentTaskFromChildSession(event.Payload)
	m.updateLayoutAndSize()

	// Subagent activity means the agent is running; keep the strip
	// spinner alive.
	if m.tasksSpinning() {
		return m.taskSpinner.Tick
	}
	return nil
}

func (m *UI) handleDialogMsg(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	action := m.dialog.Update(msg)
	if action == nil {
		return tea.Batch(cmds...)
	}

	isOnboarding := m.state == uiOnboarding

	switch msg := action.(type) {
	// Generic dialog messages
	case dialog.ActionClose:
		if isOnboarding && m.dialog.ContainsDialog(dialog.ModelsID) {
			break
		}

		// Closing the theme picker without confirming drops the preview.
		if front := m.dialog.DialogLast(); front != nil && front.ID() == dialog.ThemesID {
			m.revertThemePreview()
		}

		if m.dialog.ContainsDialog(dialog.FilePickerID) {
			defer fimage.ResetCache()
		}

		m.dialog.CloseFrontDialog()

		if isOnboarding {
			if cmd := m.openModelsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

		if m.focus == uiFocusEditor {
			cmds = append(cmds, m.focusEditor())
		}
	case dialog.ActionCmd:
		if msg.Cmd != nil {
			cmds = append(cmds, msg.Cmd)
		}

	// Session dialog messages.
	case dialog.ActionSelectSession:
		m.dialog.CloseDialog(dialog.SessionsID)
		cmds = append(cmds, m.loadSession(msg.Session.ID))

	// Open dialog message.
	case dialog.ActionOpenDialog:
		m.dialog.CloseDialog(dialog.CommandsID)
		if cmd := m.openDialog(msg.DialogID); cmd != nil {
			cmds = append(cmds, cmd)
		}

	// Server manager messages. The dialogs stay open; state events
	// refresh them as the operation takes effect.
	case dialog.ActionMCPReconnect:
		cmds = append(cmds, m.reconnectMCP(msg.Name))
	case dialog.ActionMCPDisableForSession:
		cmds = append(cmds, m.disableMCPForSession(msg.Name))
	case dialog.ActionOpenMCPAuth:
		cmds = append(cmds, m.openMCPAuthDialog())
	case dialog.ActionLSPRestart:
		cmds = append(cmds, m.restartLSP(msg.Name))
	case dialog.ActionLSPSetSessionDisabled:
		cmds = append(cmds, m.setLSPSessionDisabled(msg.Name, msg.Disabled))

	// Command dialog messages.
	case dialog.ActionSelectNotificationStyle:
		cfg := m.com.Config()
		if cfg != nil && cfg.Options != nil {
			cfg.Options.Notifications = msg.Style
			if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.notifications", msg.Style); err != nil {
				cmds = append(cmds, util.ReportError(err))
			} else {
				cmds = append(cmds, util.CmdHandler(util.NewInfoMsg("Notifications set to: "+msg.Style)))
			}
			// Reinitialize notification backend with new style.
			m.notifyBackend = selectNotificationBackend(m.caps, cfg)
		}
		m.dialog.CloseDialog(dialog.NotificationsID)
	case dialog.ActionNewSession:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
			break
		}
		if cmd := m.newSession(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionSummarize:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before summarizing session..."))
			break
		}
		cmds = append(cmds, func() tea.Msg {
			err := m.com.Workspace.AgentSummarize(context.Background(), msg.SessionID, "")
			if err != nil {
				return util.ReportError(err)()
			}
			return nil
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionCompact:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before compacting session..."))
			break
		}
		if len(msg.Arguments) > 0 && msg.Args == nil {
			m.dialog.CloseFrontDialog()
			argsDialog := dialog.NewArguments(
				m.com,
				"Compact Session",
				"Optionally steer what the compacted summary keeps.",
				msg.Arguments,
				msg,
			)
			m.dialog.OpenDialog(argsDialog)
			break
		}
		instructions := msg.Args["instructions"]
		cmds = append(cmds, func() tea.Msg {
			err := m.com.Workspace.AgentSummarize(context.Background(), msg.SessionID, instructions)
			if err != nil {
				return util.ReportError(err)()
			}
			return nil
		})
		m.dialog.CloseFrontDialog()
	case dialog.ActionSaveSummary:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before saving the summary..."))
			break
		}
		cmds = append(cmds, m.saveSummaryToFile(msg.SessionID))
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionExportConversation:
		cmds = append(cmds, m.exportConversationToFile(msg.SessionID))
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionRewindConfirmed:
		m.dialog.CloseDialog(dialog.RewindID)
		cmds = append(cmds, m.rewindSession(msg.SessionID, msg.MessageID, msg.Prompt, msg.Mode))
	case dialog.ActionToggleHelp:
		m.status.ToggleHelp()
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionExternalEditor:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is working, please wait..."))
			break
		}
		editorValue := m.textarea.Value()
		if m.bangMode {
			editorValue = "!" + editorValue
		}
		cmds = append(cmds, m.openEditor(editorValue))
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionTogglePills:
		if cmd := m.togglePillsExpanded(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleThinking:
		cmds = append(cmds, m.updateAgentModelCmd(func() tea.Msg {
			cfg := m.com.Config()
			if cfg == nil {
				return util.ReportError(errors.New("configuration not found"))()
			}

			agentCfg, ok := cfg.Agents[config.AgentCoder]
			if !ok {
				return util.ReportError(errors.New("agent configuration not found"))()
			}

			currentModel := cfg.Models[agentCfg.Model]
			currentModel.Think = !currentModel.Think
			if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, agentCfg.Model, currentModel); err != nil {
				return util.ReportError(err)()
			}
			m.com.Workspace.UpdateAgentModel(context.TODO())
			status := "disabled"
			if currentModel.Think {
				status = "enabled"
			}
			return util.NewInfoMsg("Thinking mode " + status)
		}))
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleTransparentBackground:
		cmds = append(cmds, func() tea.Msg {
			cfg := m.com.Config()
			if cfg == nil {
				return util.ReportError(errors.New("configuration not found"))()
			}

			isTransparent := cfg.Options != nil && cfg.Options.TUI.IsTransparent()
			newValue := !isTransparent
			if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.tui.transparent", newValue); err != nil {
				return util.ReportError(err)()
			}
			m.isTransparent = newValue

			status := "disabled"
			if newValue {
				status = "enabled"
			}
			return util.NewInfoMsg("Transparent background " + status)
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleMouseSupport:
		cfg := m.com.Config()
		if cfg == nil {
			cmds = append(cmds, util.ReportError(errors.New("configuration not found")))
			break
		}
		// Flip the field on the main update path so it never races with
		// View() reading m.mouseEnabled from a background command's
		// goroutine; only the (possibly slow) config write is deferred.
		mouseEnabled := cfg.Options == nil || cfg.Options.TUI.Mouse == nil || *cfg.Options.TUI.Mouse
		newValue := !mouseEnabled
		m.mouseEnabled = newValue
		cmds = append(cmds, func() tea.Msg {
			if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.tui.mouse", newValue); err != nil {
				return util.ReportError(err)()
			}

			status := "disabled"
			if newValue {
				status = "enabled"
			}
			return util.NewInfoMsg("Mouse support " + status)
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionQuit:
		cmds = append(cmds, tea.Quit)
	case dialog.ActionInitializeProject:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before summarizing session..."))
			break
		}
		cmds = append(cmds, m.initializeProject())
		m.dialog.CloseDialog(dialog.CommandsID)

	case dialog.ActionSelectModel:
		if cmd := m.handleSelectModel(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ActionSelectReasoningEffort:
		if cmd := m.handleSelectReasoningEffort(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ActionPreviewTheme:
		m.previewTheme(msg.Name)
		if msg.Cmd != nil {
			cmds = append(cmds, msg.Cmd)
		}
	case dialog.ActionSelectTheme:
		if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.tui.theme", msg.Name); err != nil {
			// The preview is still on screen but nothing was saved, so
			// put the previous theme back rather than leaving the UI in a
			// state the config doesn't describe.
			m.revertThemePreview()
			cmds = append(cmds, util.ReportError(err))
			m.dialog.CloseDialog(dialog.ThemesID)
			break
		}
		if cfg := m.com.Config(); cfg != nil && cfg.Options != nil {
			if cfg.Options.TUI == nil {
				cfg.Options.TUI = &config.TUIOptions{}
			}
			cfg.Options.TUI.Theme = msg.Name
		}
		m.previewTheme(msg.Name)
		m.commitThemePreview()
		cmds = append(cmds, util.CmdHandler(util.NewInfoMsg("Theme set to: "+msg.Name)))
		m.dialog.CloseDialog(dialog.ThemesID)
	case dialog.ActionFilePickerSelected:
		cmds = append(cmds, tea.Sequence(
			msg.Cmd(),
			func() tea.Msg {
				m.dialog.CloseDialog(dialog.FilePickerID)
				return nil
			},
			func() tea.Msg {
				fimage.ResetCache()
				return nil
			},
		))

	case dialog.ActionRunCustomCommand:
		if len(msg.Arguments) > 0 && msg.Args == nil {
			m.dialog.CloseFrontDialog()
			argsDialog := dialog.NewArguments(
				m.com,
				"Custom Command Arguments",
				"",
				msg.Arguments,
				msg, // Pass the action as the result
			)
			m.dialog.OpenDialog(argsDialog)
			break
		}
		if msg.ExtensionID != "" {
			// An extension command has no content until the extension
			// produces it, which may touch the filesystem or the network,
			// so the expansion happens off the UI loop.
			cmds = append(cmds, m.runExtensionCommand(msg.ExtensionID, msg.Args))
			m.dialog.CloseFrontDialog()
			break
		}
		content := msg.Content
		if msg.Args != nil {
			content = substituteArgs(content, msg.Args)
		}
		// If this is a skill command, format it using the skill's FormatInvocation method
		if msg.Skill != nil {
			content = msg.Skill.FormatInvocation()
		}
		cmds = append(cmds, m.sendMessage(content))
		m.dialog.CloseFrontDialog()
	case dialog.ActionAttachSkill:
		m.dialog.CloseFrontDialog()
		cmds = append(cmds, m.attachSkill(msg.ID, msg.Name))
	case dialog.ActionRunMCPPrompt:
		if len(msg.Arguments) > 0 && msg.Args == nil {
			m.dialog.CloseFrontDialog()
			title := cmp.Or(msg.Title, "MCP Prompt Arguments")
			argsDialog := dialog.NewArguments(
				m.com,
				title,
				msg.Description,
				msg.Arguments,
				msg, // Pass the action as the result
			)
			m.dialog.OpenDialog(argsDialog)
			break
		}
		cmds = append(cmds, m.runMCPPrompt(msg.ClientID, msg.PromptID, msg.Args))
	default:
		cmds = append(cmds, util.CmdHandler(msg))
	}

	return tea.Batch(cmds...)
}

// substituteArgs replaces $ARG_NAME placeholders in content with actual values.
func substituteArgs(content string, args map[string]string) string {
	for name, value := range args {
		placeholder := "$" + name
		content = strings.ReplaceAll(content, placeholder, value)
	}
	return content
}

// restoreModelFromSession checks the last assistant message in the
// loaded session and, if it used a different provider/model than the
// current config, restores that model/provider provided it is still
// available. Returns a tea.Cmd that rebuilds the agent models if a
// switch was made, or nil if no switch was needed.
func (m *UI) restoreModelFromSession(msgs []message.Message) tea.Cmd {
	var lastAssistant *message.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == message.Assistant && !msgs[i].IsSummaryMessage {
			lastAssistant = &msgs[i]
			break
		}
	}
	if lastAssistant == nil || lastAssistant.Provider == "" || lastAssistant.Model == "" {
		return nil
	}

	cfg := m.com.Config()
	if cfg == nil {
		return nil
	}

	currentLarge := cfg.Models[config.SelectedModelTypeLarge]
	if currentLarge.Provider == lastAssistant.Provider && currentLarge.Model == lastAssistant.Model {
		return nil
	}

	if !cfg.IsModelAvailable(lastAssistant.Provider, lastAssistant.Model) {
		slog.Debug("Skipping model restoration: provider/model not available",
			"provider", lastAssistant.Provider,
			"model", lastAssistant.Model)
		return nil
	}

	selectedModel := config.SelectedModel{
		Provider: lastAssistant.Provider,
		Model:    lastAssistant.Model,
	}
	if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, config.SelectedModelTypeLarge, selectedModel); err != nil {
		slog.Error("Failed to restore model from session", "error", err)
		return nil
	}

	m.applyThemeForProvider(lastAssistant.Provider)

	if _, ok := cfg.Models[config.SelectedModelTypeSmall]; !ok {
		smallModel := m.com.Workspace.GetDefaultSmallModel(lastAssistant.Provider)
		if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, config.SelectedModelTypeSmall, smallModel); err != nil {
			slog.Error("Failed to set small model during session restore", "error", err)
		}
	}

	return m.updateAgentModelCmd(func() tea.Msg {
		if err := m.com.Workspace.UpdateAgentModel(context.TODO()); err != nil {
			return util.ReportError(err)
		}
		slog.Info("Restored model from session",
			"provider", lastAssistant.Provider,
			"model", lastAssistant.Model)
		return nil
	})
}

// handleSelectReasoningEffort applies a reasoning effort selection.
// While the agent is mid-turn the choice is queued and applied when
// the turn finishes, so the in-flight request is untouched.
func (m *UI) handleSelectReasoningEffort(msg dialog.ActionSelectReasoningEffort) tea.Cmd {
	if m.isAgentBusy() {
		m.pendingModelAction = msg
		m.dialog.CloseDialog(dialog.ReasoningID)
		return func() tea.Msg {
			return util.NewInfoMsg("Reasoning effort applies after the current turn")
		}
	}

	cfg := m.com.Config()
	if cfg == nil {
		return util.ReportError(errors.New("configuration not found"))
	}

	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return util.ReportError(errors.New("agent configuration not found"))
	}

	currentModel := cfg.Models[agentCfg.Model]
	currentModel.ReasoningEffort = msg.Effort
	if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, agentCfg.Model, currentModel); err != nil {
		return util.ReportError(err)
	}

	// Remember the choice per model so switching models and back
	// restores it instead of leaking the current model's effort. The
	// whole map is written in one field: model IDs contain dots, which
	// the dot-separated field paths cannot express.
	if currentModel.Provider != "" && currentModel.Model != "" {
		efforts := maps.Clone(cfg.ReasoningEfforts)
		if efforts == nil {
			efforts = make(map[string]string)
		}
		efforts[config.ModelReasoningKey(currentModel.Provider, currentModel.Model)] = msg.Effort
		if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "reasoning_efforts", efforts); err != nil {
			return util.ReportError(err)
		}
	}

	m.dialog.CloseDialog(dialog.ReasoningID)
	return m.updateAgentModelCmd(func() tea.Msg {
		m.com.Workspace.UpdateAgentModel(context.TODO())
		return util.NewInfoMsg("Reasoning effort set to " + msg.Effort)
	})
}

// applyPendingModelAction applies a model or effort selection queued
// while the agent was mid-turn. Called on the busy-to-idle edge so the
// choice lands between turns.
func (m *UI) applyPendingModelAction() tea.Cmd {
	pending := m.pendingModelAction
	m.pendingModelAction = nil
	switch action := pending.(type) {
	case dialog.ActionSelectModel:
		return m.handleSelectModel(action)
	case dialog.ActionSelectReasoningEffort:
		return m.handleSelectReasoningEffort(action)
	}
	return nil
}

// handleSelectModel performs the model selection after any provider
// pre-checks have completed.
func (m *UI) handleSelectModel(msg dialog.ActionSelectModel) tea.Cmd {
	var cmds []tea.Cmd

	// we ignore dialogs with the oauth id as they need to be able to be dismissed
	if m.isAgentBusy() && !m.dialog.ContainsDialog(dialog.OAuthID) {
		// The in-flight turn keeps the model it started with; the
		// choice is queued and applied when the turn finishes.
		m.pendingModelAction = msg
		m.dialog.CloseDialog(dialog.ModelsID)
		return func() tea.Msg {
			return util.NewInfoMsg("Model change applies after the current turn")
		}
	}

	cfg := m.com.Config()
	if cfg == nil {
		return util.ReportError(errors.New("configuration not found"))
	}

	var (
		providerID   = msg.Model.Provider
		isCopilot    = providerID == string(catalog.InferenceProviderCopilot)
		isConfigured = func() bool { _, ok := cfg.Providers.Get(providerID); return ok }
		isOnboarding = m.state == uiOnboarding
	)

	// Attempt to import GitHub Copilot tokens from VSCode if available.
	if isCopilot && !isConfigured() && !msg.ReAuthenticate {
		m.com.Workspace.ImportCopilot()
	}

	if !isConfigured() || msg.ReAuthenticate {
		m.dialog.CloseDialog(dialog.ModelsID)
		m.dialog.CloseDialog(dialog.ConnectID)
		if cmd := m.openAuthenticationDialog(msg.Provider, msg.Model, msg.ModelType); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return tea.Batch(cmds...)
	}

	// Restore a remembered per-model reasoning effort so manual choices
	// survive model switches; without one the selection defaults to the
	// model's highest supported level.
	if effort, ok := cfg.ReasoningEfforts[config.ModelReasoningKey(msg.Model.Provider, msg.Model.Model)]; ok {
		msg.Model.ReasoningEffort = effort
	}

	if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, msg.ModelType, msg.Model); err != nil {
		cmds = append(cmds, util.ReportError(err))
	} else {
		if msg.ModelType == config.SelectedModelTypeLarge {
			// Swap the theme live based on the newly selected large
			// model's provider. Skipped when the provider resolves to
			// the already-active theme, which avoids a full markdown
			// re-render of the transcript on every selection.
			m.applyThemeForProvider(providerID)
		}
		if _, ok := cfg.Models[config.SelectedModelTypeSmall]; !ok {
			// Ensure small model is set is unset.
			smallModel := m.com.Workspace.GetDefaultSmallModel(providerID)
			if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, config.SelectedModelTypeSmall, smallModel); err != nil {
				cmds = append(cmds, util.ReportError(err))
			}
		}
	}

	cmds = append(cmds, m.updateAgentModelCmd(func() tea.Msg {
		if err := m.com.Workspace.UpdateAgentModel(context.TODO()); err != nil {
			return util.ReportError(err)
		}

		var (
			modelType = stringext.Capitalize(string(msg.ModelType))
			modelName = msg.Model.Model
		)
		if catwalkModel := cfg.GetModel(msg.Model.Provider, msg.Model.Model); catwalkModel != nil && catwalkModel.Name != "" {
			modelName = catwalkModel.Name
		}
		modelMsg := fmt.Sprintf("%s model changed to %s", modelType, modelName)

		return util.NewInfoMsg(modelMsg)
	}))

	m.dialog.CloseDialog(dialog.APIKeyInputID)
	m.dialog.CloseDialog(dialog.OAuthID)
	m.dialog.CloseDialog(dialog.ModelsID)
	m.dialog.CloseDialog(dialog.ConnectID)

	if isOnboarding {
		m.setState(uiLanding, uiFocusEditor)
		m.com.Config().SetupAgents()
		if err := m.com.Workspace.InitCoderAgent(context.TODO()); err != nil {
			cmds = append(cmds, util.ReportError(err))
		}
		// The agent just came up: re-fetch the memoized ready/model state
		// so the landing view shows the selected model without waiting for
		// the TTL backstop.
		m.invalidateBusyCaches()
		if cmd := m.dispatchBusyRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	return tea.Batch(cmds...)
}

func (m *UI) openAuthenticationDialog(provider catalog.Provider, model config.SelectedModel, modelType config.SelectedModelType) tea.Cmd {
	var (
		dlg dialog.Dialog
		cmd tea.Cmd

		isOnboarding = m.state == uiOnboarding
	)

	switch provider.ID {
	case catalog.InferenceProviderCopilot:
		dlg, cmd = dialog.NewOAuthCopilot(m.com, isOnboarding, provider, model, modelType)
	default:
		dlg, cmd = dialog.NewAPIKeyInput(m.com, isOnboarding, provider, model, modelType)
	}

	if m.dialog.ContainsDialog(dlg.ID()) {
		m.dialog.BringToFront(dlg.ID())
		return nil
	}

	m.dialog.OpenDialogWithGrace(dlg)
	return cmd
}

func (m *UI) handleKeyPressMsg(msg tea.KeyPressMsg) tea.Cmd {
	var cmds []tea.Cmd

	handleGlobalKeys := func(msg tea.KeyPressMsg) bool {
		switch {
		case key.Matches(msg, m.keyMap.Chat.BackgroundTasks):
			if m.focus == uiFocusTasks {
				m.focusEditorFromTasks()
			} else if m.state == uiChat && len(m.agentTasks) > 0 {
				m.focusTasks()
			}
			return true
		case key.Matches(msg, m.keyMap.Help):
			m.status.ToggleHelp()
			m.updateLayoutAndSize()
			return true
		case key.Matches(msg, m.keyMap.Commands):
			if cmd := m.openCommandsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Models):
			if cmd := m.openModelsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Sessions):
			if cmd := m.openSessionsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Themes):
			if cmd := m.openThemesDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Chat.Details):
			if m.state == uiChat && m.hasSession() {
				m.toggleDetails()
			}
			return true
		case key.Matches(msg, m.keyMap.Chat.EndFollow):
			if m.state == uiChat && m.hasSession() {
				if cmd := m.chat.ScrollToBottomAndSelectLast(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Chat.TogglePills):
			if m.state == uiChat && m.hasSession() {
				if cmd := m.togglePillsExpanded(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Suspend):
			if m.isAgentBusy() {
				cmds = append(cmds, util.ReportWarn("Agent is busy, please wait..."))
				return true
			}
			cmds = append(cmds, tea.Suspend)
			return true
		case key.Matches(msg, m.keyMap.ParentSession):
			if m.session != nil && m.session.ParentSessionID != "" {
				cmds = append(cmds, m.loadSession(m.session.ParentSessionID))
				return true
			}
		case key.Matches(msg, m.keyMap.ExportConversation):
			if m.hasSession() {
				cmds = append(cmds, m.exportConversationToFile(m.session.ID))
				return true
			}
		}
		return false
	}

	if key.Matches(msg, m.keyMap.Quit) {
		// Always handle quit keys first: the first press arms a short
		// window, a second press within it quits without a dialog.
		if cmd := m.quit(); cmd != nil {
			cmds = append(cmds, cmd)
		}

		return tea.Batch(cmds...)
	}

	// Route all messages to dialog if one is open.
	if m.dialog.HasDialogs() {
		return m.handleDialogMsg(msg)
	}

	// Tab always toggles focus between editor and chat, even when
	// an inline editor is active. This lets users collapse the
	// question form to view chat.
	if m.activeInline != nil && key.Matches(msg, m.keyMap.Tab) {
		if m.focus == uiFocusEditor {
			m.focus = uiFocusMain
			m.activeInline.SetFocused(false)
			cmds = append(cmds, m.chat.FocusRestoringSelection())
		} else {
			cmds = append(cmds, m.focusEditor())
		}
		m.updateLayoutAndSize()
		return tea.Batch(cmds...)
	}

	// Route keys to active inline editor if one is showing.
	if m.activeInline != nil && m.focus == uiFocusEditor {
		if done, cmd := m.activeInline.HandleKey(msg); done {
			m.activeInline = nil
			cmds = append(cmds, m.focusEditor())
			m.updateLayoutAndSize()
		} else {
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			if m.activeInline.HeightChanged() {
				m.updateLayoutAndSize()
			}
		}
		return tea.Batch(cmds...)
	}

	// Escape cancels a busy run only while the editor is focused. From
	// the chat or the background tasks strip it goes out a level instead
	// (collapsing an expanded group, call, or task) and never cancels.
	if key.Matches(msg, m.keyMap.Chat.Cancel) && m.focus == uiFocusEditor && m.isAgentBusy() {
		if cmd := m.cancelAgent(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return tea.Batch(cmds...)
	}

	switch m.state {
	case uiOnboarding:
		return tea.Batch(cmds...)
	case uiChat, uiLanding:
		switch m.focus {
		case uiFocusEditor:
			// Handle completions if open.
			if m.completionsOpen {
				if msg, ok := m.completions.Update(msg); ok {
					switch msg := msg.(type) {
					case completions.SelectionMsg[completions.FileCompletionValue]:
						cmds = append(cmds, m.insertFileCompletion(msg.Value.Path))
						if !msg.KeepOpen {
							m.closeCompletions()
						}
					case completions.SelectionMsg[completions.ResourceCompletionValue]:
						cmds = append(cmds, m.insertMCPResourceCompletion(msg.Value))
						if !msg.KeepOpen {
							m.closeCompletions()
						}
					case completions.SelectionMsg[completions.SubagentCompletionValue]:
						cmds = append(cmds, m.insertSubagentCompletion(msg.Value.Name))
						if !msg.KeepOpen {
							m.closeCompletions()
						}
					case completions.ClosedMsg:
						m.completionsOpen = false
					}
					return tea.Batch(cmds...)
				}
			}

			if ok := m.attachments.Update(msg); ok {
				m.updateLayoutAndSize()
				return tea.Batch(cmds...)
			}

			switch {
			case key.Matches(msg, m.keyMap.Editor.AddImage):
				if !m.currentModelSupportsImages() {
					break
				}
				if cmd := m.openFilesDialog(); cmd != nil {
					cmds = append(cmds, cmd)
				}

			case key.Matches(msg, m.keyMap.Editor.PasteImage):
				if !m.currentModelSupportsImages() {
					break
				}
				cmds = append(cmds, m.pasteImageFromClipboard)
			case key.Matches(msg, m.keyMap.Editor.PasteText):
				cmds = append(cmds, m.pasteTextFromClipboard)

			case key.Matches(msg, m.keyMap.Editor.SendMessage):
				prevHeight := m.textarea.Height()
				value := m.textarea.Value()
				if before, ok := strings.CutSuffix(value, "\\"); ok {
					// If the last character is a backslash, remove it and add a newline.
					m.textarea.SetValue(before)
					if cmd := m.handleTextareaHeightChange(prevHeight); cmd != nil {
						cmds = append(cmds, cmd)
					}
					break
				}

				// Otherwise, send the message
				m.textarea.Reset()
				if cmd := m.handleTextareaHeightChange(prevHeight); cmd != nil {
					cmds = append(cmds, cmd)
				}

				value = strings.TrimSpace(value)
				if value == "exit" || value == "quit" {
					return tea.Quit
				}

				if m.bangMode && value != "" {
					m.bangMode = false
					m.setEditorPrompt()
					m.randomizePlaceholders()
					m.historyReset()
					return tea.Batch(m.runShellCommand(value))
				}

				attachments := m.attachments.List()
				m.attachments.Reset()
				// Pasted attachments ride inline tokens in the text; swap
				// them for the payloads and strip the tokens.
				value, pasted := m.resolvePastedAttachments(value)
				attachments = append(attachments, pasted...)
				if len(value) == 0 && !message.ContainsTextAttachment(attachments) {
					return nil
				}

				m.randomizePlaceholders()
				m.historyReset()

				return tea.Batch(m.sendMessage(value, attachments...), m.loadPromptHistory())
			case key.Matches(msg, m.keyMap.Chat.NewSession):
				if !m.hasSession() {
					break
				}
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
					break
				}
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Tab):
				if m.state != uiLanding {
					m.setState(m.state, uiFocusMain)
					m.textarea.Blur()
					cmds = append(cmds, m.chat.FocusRestoringSelection())
				}
			case key.Matches(msg, m.keyMap.ShiftTab):
				if cmd := m.focusAboveEditor(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Chat.UpOneItem) && msg.String() == "shift+up":
				// Shift+up leaves the editor the same way shift+tab does.
				// Only the arrow key qualifies: the binding's letter alias
				// (K) must stay typeable.
				if cmd := m.focusAboveEditor(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.OpenEditor):
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is working, please wait..."))
					break
				}
				editorValue := m.textarea.Value()
				if m.bangMode {
					editorValue = "!" + editorValue
				}
				cmds = append(cmds, m.openEditor(editorValue))
			case key.Matches(msg, m.keyMap.Editor.Newline):
				prevHeight := m.textarea.Height()
				m.textarea.InsertRune('\n')
				m.closeCompletions()
				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))
			case key.Matches(msg, m.keyMap.Editor.CopySelection):
				if m.textarea.HasSelection() {
					cmds = append(cmds, common.CopyToClipboardWithCallback(
						m.textarea.SelectedText(),
						"Selection copied to clipboard",
						nil,
					))
					m.textarea.ClearSelection()
				}
			case key.Matches(msg, m.keyMap.Editor.CutSelection):
				if m.textarea.HasSelection() {
					cmds = append(cmds, common.CopyToClipboardWithCallback(
						m.textarea.SelectedText(),
						"Selection cut to clipboard",
						nil,
					))
					m.textarea.DeleteSelection()
				}
			case key.Matches(msg, m.keyMap.Editor.HistoryPrev):
				cmd := m.handleHistoryUp(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.HistoryNext):
				cmd := m.handleHistoryDown(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.Escape):
				if consumed, cmd := m.handleRewindEscape(); consumed {
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
					break
				}
				cmd := m.handleHistoryEscape(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.Skills) && m.textarea.Value() == "":
				if cmd := m.openSkillsDialog(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.Commands) && m.textarea.Value() == "":
				if cmd := m.openCommandsDialog(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			default:
				if handleGlobalKeys(msg) {
					// Handle global keys first before passing to textarea.
					break
				}

				// Bang mode: backspace on already-empty prompt exits.
				if m.bangMode && m.bangWasEmpty && msg.Code == tea.KeyBackspace {
					m.bangMode = false
					m.bangWasEmpty = false
					m.setEditorPrompt()
					break
				}

				// Check for @ trigger before passing to textarea.
				curValue := m.textarea.Value()
				curIdx := len(curValue)

				// Trigger completions on the mention key.
				if key.Matches(msg, m.keyMap.Editor.MentionFile) && !m.completionsOpen {
					// Only show if beginning of prompt or after whitespace.
					if curIdx == 0 || (curIdx > 0 && isWhitespace(curValue[curIdx-1])) {
						m.completionsOpen = true
						m.completionsQuery = ""
						m.completionsStartIndex = curIdx
						m.completionsPositionStart = m.completionsPosition()
						depth, limit := m.com.Config().Options.TUI.Completions.Limits()
						cmds = append(cmds, m.completions.Open(depth, limit, m.activeSubagentItems))
					}
				}

				prevHeight := m.textarea.Height()
				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))

				// Bang mode: enter when the shell-mode key ("!") is typed
				// at the start of the prompt, optionally preceded by
				// whitespace (either on an empty/whitespace-only prompt or
				// prepended to existing text). Exit on backspace clearing
				// the last character.
				newVal := m.textarea.Value()
				trimmedNew := strings.TrimLeftFunc(newVal, unicode.IsSpace)
				trimmedCur := strings.TrimLeftFunc(curValue, unicode.IsSpace)
				if !m.bangMode && key.Matches(msg, m.keyMap.Editor.ShellMode) && strings.HasPrefix(trimmedNew, "!") && !strings.HasPrefix(trimmedCur, "!") {
					m.bangMode = true
					m.bangWasEmpty = len(strings.TrimSpace(curValue)) == 0
					// Strip leading whitespace and the "!" from the textarea
					// while preserving the cursor position relative to the
					// command text.
					col := m.textarea.Column()
					line := m.textarea.Line()
					stripped := trimmedNew[1:]
					m.textarea.SetValue(stripped)
					m.textarea.SetCursorColumn(max(0, col-(len(newVal)-len(stripped))))
					_ = line // cursor line doesn't change; prefix removed
					m.setEditorPrompt()
				} else if m.bangMode && newVal == "" && curValue != "" {
					// Just cleared last character; mark empty, stay in bang mode.
					m.bangWasEmpty = true
				} else if m.bangMode && newVal != "" {
					m.bangWasEmpty = false
				}

				// Any text modification becomes the current draft.
				m.updateHistoryDraft(curValue)

				// After updating textarea, check if we need to filter completions.
				// Skip filtering on the initial mention keystroke since items are loading async.
				if m.completionsOpen && !key.Matches(msg, m.keyMap.Editor.MentionFile) {
					newValue := m.textarea.Value()
					newIdx := len(newValue)

					// Close completions if cursor moved before start.
					if newIdx <= m.completionsStartIndex {
						m.closeCompletions()
					} else if msg.String() == "space" {
						// Close on space.
						m.closeCompletions()
					} else {
						// Extract current word and filter.
						word := m.textareaWord()
						if strings.HasPrefix(word, "@") {
							m.completionsQuery = word[1:]
							m.completions.Filter(m.completionsQuery)
						} else if m.completionsOpen {
							m.closeCompletions()
						}
					}
				}
			}
		case uiFocusMain:
			switch {
			case key.Matches(msg, m.keyMap.Tab):
				// Tab cycles chat -> background tasks (when present) -> editor.
				if m.state == uiChat && len(m.agentTasks) > 0 {
					m.chat.Blur()
					m.focusTasks()
				} else {
					cmds = append(cmds, m.focusEditor())
				}
			case key.Matches(msg, m.keyMap.ShiftTab):
				cmds = append(cmds, m.focusEditor())
			case key.Matches(msg, m.keyMap.Chat.NewSession):
				if !m.hasSession() {
					break
				}
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
					break
				}
				cmds = append(cmds, m.focusEditor(), m.newSession())
			case key.Matches(msg, m.keyMap.Chat.Expand):
				m.chat.ToggleExpandedSelectedItem()
			case key.Matches(msg, m.keyMap.Chat.DigIn):
				m.chat.EnterSelectedItem()
			case key.Matches(msg, m.keyMap.Chat.ClearHighlight):
				// Escape goes out one level: a fully rendered call
				// collapses, then the open group. At the top level it
				// keeps its existing meaning.
				m.chat.AscendSelectedItem()
			case key.Matches(msg, m.keyMap.Chat.Up):
				if m.chat.SubCursorUp() {
					break
				}
				m.markScrollOnly()
				m.chat.ScrollBy(-1)
				if !m.chat.SelectedItemInView() {
					m.chat.SelectPrev()
					m.chat.ScrollToSelected()
				}
			case key.Matches(msg, m.keyMap.Chat.Down):
				if m.chat.SubCursorDown() {
					break
				}
				m.markScrollOnly()
				m.chat.ScrollBy(1)
				if !m.chat.SelectedItemInView() {
					m.chat.SelectNext()
					m.chat.ScrollToSelected()
				}
			case key.Matches(msg, m.keyMap.Chat.UpOneItem):
				if m.chat.SubCursorUp() {
					break
				}
				m.chat.SelectPrev()
				m.chat.ScrollToSelected()
			case key.Matches(msg, m.keyMap.Chat.DownOneItem):
				if m.chat.SubCursorDown() {
					break
				}
				if m.chat.SelectNext() {
					m.chat.ScrollToSelected()
					break
				}
				// At the newest item: continue into the region below the
				// transcript.
				if cmd := m.focusBelowChat(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Chat.HalfPageUp):
				m.markScrollOnly()
				m.chat.ScrollBy(-m.chat.Height() / 2)
				m.chat.SelectFirstInView()
			case key.Matches(msg, m.keyMap.Chat.HalfPageDown):
				m.markScrollOnly()
				m.chat.ScrollBy(m.chat.Height() / 2)
				m.chat.SelectLastInView()
			case key.Matches(msg, m.keyMap.Chat.PageUp):
				m.markScrollOnly()
				m.chat.ScrollBy(-m.chat.Height())
				m.chat.SelectFirstInView()
			case key.Matches(msg, m.keyMap.Chat.PageDown):
				m.markScrollOnly()
				m.chat.ScrollBy(m.chat.Height())
				m.chat.SelectLastInView()
			case key.Matches(msg, m.keyMap.Chat.Home):
				m.chat.ScrollToTop()
				m.chat.SelectFirst()
			case key.Matches(msg, m.keyMap.Chat.End):
				m.chat.ScrollToBottomAndSelectLast()
			default:
				if ok, cmd := m.chat.HandleKeyMsg(msg); ok {
					cmds = append(cmds, cmd)
				} else {
					handleGlobalKeys(msg)
				}
			}
		case uiFocusTasks:
			// The background tasks strip: up/down move the cursor through
			// tasks and their calls, enter/space expands, esc or tab leaves.
			if m.state != uiChat || len(m.agentTasks) == 0 {
				m.focusEditorFromTasks()
				break
			}
			if consumed, cmd := m.handleTaskKey(msg); consumed {
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
				break
			}
			handleGlobalKeys(msg)
		default:
			handleGlobalKeys(msg)
		}
	default:
		handleGlobalKeys(msg)
	}

	return tea.Sequence(cmds...)
}

// drawHeader draws the header section of the UI.
func (m *UI) drawHeader(scr uv.Screen, area uv.Rectangle) {
	if m.header == nil {
		return
	}
	m.header.drawHeader(
		scr,
		area,
		m.session,
		area.Dx(),
		m.lspDiagnosticTotals(),
		parentBreadcrumbLine(m.com.Styles, m.subagentColor, m.parentTitle, area.Dx()),
	)
}

// Draw implements [uv.Drawable] and draws the UI model.
func (m *UI) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	layout := m.generateLayout(area.Dx(), area.Dy())

	if m.layout != layout {
		m.layout = layout
		m.updateSize()
	} else if m.state == uiChat && m.hasSession() {
		// Re-render pills on every draw so the box appears even when
		// the layout footprint hasn't changed (e.g. todos arrived
		// while the panel was collapsed). updateSize already calls
		// renderPills, but only when the layout actually differs;
		// this catches the steady-state case.
		m.renderPills()
	}

	// Clear the screen first
	screen.Clear(scr)

	switch m.state {
	case uiOnboarding:
		m.drawHeader(scr, layout.header)

		// NOTE: Onboarding flow will be rendered as dialogs below, but
		// positioned at the bottom left of the screen.

	case uiLanding:
		m.drawHeader(scr, layout.header)
		main := uv.NewStyledString(m.landingView())
		main.Draw(scr, layout.main)
		m.drawEditorArea(scr, layout.editor)

	case uiChat:
		m.drawHeader(scr, layout.header)

		m.chat.Draw(scr, layout.main)
		if layout.tasks.Dy() > 0 && m.tasksView != "" {
			uv.NewStyledString(m.tasksView).Draw(scr, layout.tasks)
		}
		if layout.pills.Dy() > 0 && m.pillsView != "" {
			uv.NewStyledString(m.pillsView).Draw(scr, layout.pills)
		}

		m.drawEditorArea(scr, layout.editor)
	}

	isOnboarding := m.state == uiOnboarding

	// Add status and help layer
	m.status.Draw(scr, layout.status)

	// Draw completions popup if open
	if !isOnboarding && m.completionsOpen && m.completions.HasItems() {
		w, h := m.completions.Size()
		x := m.completionsPositionStart.X
		y := m.completionsPositionStart.Y - h

		screenW := area.Dx()
		if x+w > screenW {
			x = screenW - w
		}
		x = max(0, x)
		y = max(0, y+1) // Offset for attachments row

		completionsView := uv.NewStyledString(m.completions.Render())
		completionsView.Draw(scr, image.Rectangle{
			Min: image.Pt(x, y),
			Max: image.Pt(x+w, y+h),
		})
	}

	// Debugging rendering (visually see when the tui rerenders)
	if os.Getenv("HARNESS_UI_DEBUG") == "true" {
		debugView := lipgloss.NewStyle().Background(lipgloss.ANSIColor(rand.Intn(256))).Width(4).Height(2)
		debug := uv.NewStyledString(debugView.String())
		debug.Draw(scr, image.Rectangle{
			Min: image.Pt(4, 1),
			Max: image.Pt(8, 3),
		})
	}

	// This needs to come last to overlay on top of everything. We always pass
	// the full screen bounds because the dialogs will position themselves
	// accordingly.
	if m.dialog.HasDialogs() {
		return m.dialog.Draw(scr, scr.Bounds())
	}

	switch m.focus {
	case uiFocusEditor:
		if m.layout.editor.Dy() <= 0 {
			// Don't show cursor if editor is not visible
			return nil
		}

		if m.activeInline != nil {
			// Editor may not start at the screen edge; the inline
			// editor draws from the content origin, below the top
			// frame line.
			origin := m.editorContentOrigin()
			return common.OffsetCursor(m.inlineCursor, origin.X, origin.Y, 0, 0)
		}

		if m.textarea.Focused() {
			// Editor may not start at the screen edge; the origin
			// carries the attachments-strip offset so the cursor sits
			// on the field itself.
			origin := m.textareaOrigin()
			return common.OffsetCursor(m.textarea.Cursor(), origin.X, origin.Y, 0, 0)
		}
	}
	return nil
}

// mouseMode determines the Bubble Tea mouse reporting mode to request for
// the current frame. When mouse support is disabled via configuration, no
// mouse mode is requested so the terminal emulator (or tmux) can handle
// text selection, copy/paste, and scrolling natively. Inline editors need
// motion events even without a button pressed (e.g. for hover/drag), so
// they use MouseModeAllMotion; everything else only needs click/drag
// tracking via MouseModeCellMotion.
func mouseMode(enabled, inlineActive bool) tea.MouseMode {
	switch {
	case !enabled:
		return tea.MouseModeNone
	case inlineActive:
		return tea.MouseModeAllMotion
	default:
		return tea.MouseModeCellMotion
	}
}

// View renders the UI model's view.
func (m *UI) View() tea.View {
	var v tea.View
	v.AltScreen = true
	if !m.isTransparent {
		v.BackgroundColor = m.com.Styles.Background
	}
	v.MouseMode = mouseMode(m.mouseEnabled, m.activeInline != nil)
	v.ReportFocus = m.caps.ReportFocusEvents
	v.WindowTitle = home.Short(m.com.Workspace.WorkingDir())

	key, cacheable := m.currentFrameKey()
	if cacheable {
		if content, cursor, ok := m.frames.get(key); ok {
			v.Content = content
			v.Cursor = cursor
			m.applyProgressBar(&v)
			return v
		}
	}

	canvas := uv.NewScreenBuffer(m.width, m.height)
	v.Cursor = m.Draw(canvas, canvas.Bounds())

	content := strings.ReplaceAll(canvas.Render(), "\r\n", "\n") // normalize newlines
	contentLines := strings.Split(content, "\n")
	for i, line := range contentLines {
		// Trim trailing spaces for concise rendering
		contentLines[i] = strings.TrimRight(line, " ")
	}

	content = strings.Join(contentLines, "\n")

	v.Content = content
	if cacheable {
		m.storeFrame(key, content, v.Cursor)
	}
	m.applyProgressBar(&v)

	return v
}

// applyProgressBar attaches the terminal progress bar while the agent is
// busy. Kept outside the frame cache so the randomized value stays fresh.
func (m *UI) applyProgressBar(v *tea.View) {
	if m.progressBarEnabled && m.sendProgressBar && m.isAgentBusy() {
		// HACK: use a random percentage to prevent ghostty from hiding it
		// after a timeout.
		v.ProgressBar = tea.NewProgressBar(tea.ProgressBarIndeterminate, rand.Intn(100))
	}
}

// editorPrefixHints returns the editor's first-character triggers: "!"
// enters shell mode, ":" opens the command palette and "/" the skills
// palette. They only work as the editor's first character and none applies
// while shell mode is already active, so they are hinted only while the
// editor is empty and idle.
func editorPrefixHints(k *KeyMap, show bool) []key.Binding {
	if !show {
		return nil
	}
	return []key.Binding{k.Editor.ShellMode, k.Editor.Commands, k.Editor.Skills}
}

// attachmentHelpBinds returns the attachment bindings that work in the
// current mode, so the help never advertises an attachment key the live
// routing would not honor. ctrl+r arms delete mode whenever attachments
// exist; once armed, esc leaves it and r clears every attachment. While a
// busy cancel would consume esc first (it is checked before delete mode),
// the esc hint steps aside.
func attachmentHelpBinds(k *KeyMap, hasAttachments, deleting, busy bool) []key.Binding {
	switch {
	case deleting && !busy:
		return []key.Binding{k.Editor.Escape, k.Editor.DeleteAllAttachments}
	case deleting:
		return []key.Binding{k.Editor.DeleteAllAttachments}
	case hasAttachments:
		return []key.Binding{k.Editor.AttachmentDeleteMode}
	default:
		return nil
	}
}

// ShortHelp implements [help.KeyMap].
func (m *UI) ShortHelp() []key.Binding {
	var binds []key.Binding
	k := &m.keyMap
	deleting := m.attachments.Deleting()

	// A dialog owns the keyboard while it is open, so the hints are its
	// own; the bottom-anchored panels leave that status line visible.
	if m.dialog.HasDialogs() {
		return m.dialog.ShortHelp()
	}

	// When an inline editor owns the editor area, its hints describe the
	// keys it handles — and it only handles keys while it is focused, so
	// show the chat's own hints once focus moves away.
	if m.activeInline != nil && m.focus == uiFocusEditor {
		return m.activeInline.ShortHelp()
	}

	tab := k.Tab
	// "!" enters shell mode and ":" and "/" open the command and skills
	// palettes only as the editor's first character, so they are hinted
	// beside commands only while the editor is empty and idle.
	showEditorPalettes := m.focus == uiFocusEditor && m.textarea.Value() == "" && !m.bangMode

	switch m.state {
	case uiChat:
		// Show cancel binding if agent is busy. Esc cancels only from
		// the editor, so hint it only there. The cancel check runs
		// before attachment delete mode consumes esc, so the hint stays
		// accurate while that mode is armed.
		if m.isAgentBusy() && m.focus == uiFocusEditor {
			cancelBinding := k.Chat.Cancel
			if m.isCanceling {
				cancelBinding.SetHelp(keys.HelpKeys(cancelBinding), "press again to cancel")
			}
			binds = append(binds, cancelBinding)
		} else if m.focus == uiFocusEditor && m.rewindEscArmed && !deleting {
			// Idle with the first escape pressed: the next one opens the
			// rewind picker. Armed delete mode consumes esc first, so the
			// rewind hint yields while it is up.
			rewindBinding := k.Chat.Cancel
			rewindBinding.SetHelp(keys.HelpKeys(rewindBinding), "press again to rewind")
			binds = append(binds, rewindBinding)
		}

		switch m.focus {
		case uiFocusEditor:
			tab.SetHelp(keys.HelpKeys(tab), "focus chat")
		default:
			tab.SetHelp(keys.HelpKeys(tab), "focus editor")
		}

		binds = append(binds, tab, k.Commands)
		binds = append(binds, editorPrefixHints(k, showEditorPalettes)...)
		binds = append(binds, k.Models)
		// Details only routes in a chat session, so it is only hinted there;
		// the details dialog itself declares the close/toggle hints while open.
		if m.hasSession() {
			binds = append(binds, k.Chat.Details)
		}

		switch m.focus {
		case uiFocusEditor:
			binds = append(
				binds,
				k.Editor.Newline,
			)
			binds = append(binds, attachmentHelpBinds(k, m.hasAttachments(), deleting, m.isAgentBusy())...)
		case uiFocusMain:
			binds = append(
				binds,
				k.Chat.UpDown,
				k.Chat.UpDownOneItem,
				k.Chat.PageUp,
				k.Chat.PageDown,
				k.Chat.Copy,
			)
		case uiFocusTasks:
			binds = append(
				binds,
				k.Chat.UpDown,
				k.Chat.Expand,
				k.Chat.ClearHighlight,
			)
		}
	default:
		// TODO: other states
		// if m.session == nil {
		// no session selected
		binds = append(
			binds,
			k.Commands,
			k.Models,
			k.Editor.Newline,
		)
		binds = append(binds, editorPrefixHints(k, showEditorPalettes)...)
		if m.focus == uiFocusEditor {
			binds = append(binds, attachmentHelpBinds(k, m.hasAttachments(), deleting, m.isAgentBusy())...)
		}
	}

	quit := k.Quit
	if m.isQuitting {
		quit.SetHelp(keys.HelpKeys(quit), "press again to quit")
	}
	binds = append(
		binds,
		quit,
		k.Help,
	)

	return binds
}

// FullHelp implements [help.KeyMap].
func (m *UI) FullHelp() [][]key.Binding {
	// A dialog owns the keyboard while it is open, so the hints are its
	// own; the bottom-anchored panels leave that status line visible.
	if m.dialog.HasDialogs() {
		return m.dialog.FullHelp()
	}

	// When an inline editor owns the editor area, its hints describe the
	// keys it handles — and it only handles keys while it is focused.
	if m.activeInline != nil && m.focus == uiFocusEditor {
		return [][]key.Binding{m.activeInline.ShortHelp()}
	}

	var binds [][]key.Binding
	k := &m.keyMap
	deleting := m.attachments.Deleting()
	help := k.Help
	help.SetHelp(keys.HelpKeys(help), "less")
	hasAttachments := m.hasAttachments()
	hasSession := m.hasSession()
	// "!" enters shell mode and ":" and "/" open the command and skills
	// palettes only as the editor's first character, so they are hinted
	// beside commands only while the editor is empty and idle.
	showEditorPalettes := m.focus == uiFocusEditor && m.textarea.Value() == "" && !m.bangMode

	switch m.state {
	case uiChat:
		// Show cancel binding if agent is busy; esc cancels only from
		// the editor, and the cancel check runs before delete mode can
		// consume esc, so the hint stays accurate either way.
		if m.isAgentBusy() && m.focus == uiFocusEditor {
			cancelBinding := k.Chat.Cancel
			if m.isCanceling {
				cancelBinding.SetHelp(keys.HelpKeys(cancelBinding), "press again to cancel")
			}
			binds = append(binds, []key.Binding{cancelBinding})
		} else if m.focus == uiFocusEditor && m.rewindEscArmed && !deleting {
			rewindBinding := k.Chat.Cancel
			rewindBinding.SetHelp(keys.HelpKeys(rewindBinding), "press again to rewind")
			binds = append(binds, []key.Binding{rewindBinding})
		}

		mainBinds := []key.Binding{}
		tab := k.Tab
		switch m.focus {
		case uiFocusEditor:
			tab.SetHelp(keys.HelpKeys(tab), "focus chat")
		default:
			tab.SetHelp(keys.HelpKeys(tab), "focus editor")
		}

		mainBinds = append(
			mainBinds,
			tab,
			k.Commands,
		)
		mainBinds = append(mainBinds, editorPrefixHints(k, showEditorPalettes)...)
		mainBinds = append(
			mainBinds,
			k.Models,
			k.Sessions,
			k.Themes,
		)
		if hasSession {
			mainBinds = append(mainBinds, k.Chat.NewSession, k.Chat.EndFollow, k.ExportConversation, k.Chat.Details)
		}

		binds = append(binds, mainBinds)

		switch m.focus {
		case uiFocusEditor:
			editorBinds := []key.Binding{
				k.Editor.Newline,
				k.Editor.MentionFile,
				k.Editor.OpenEditor,
				k.Editor.PasteText,
				k.Editor.SelectAll,
				k.Editor.CopySelection,
				k.Editor.CutSelection,
			}
			if m.currentModelSupportsImages() {
				editorBinds = append(editorBinds, k.Editor.AddImage, k.Editor.PasteImage)
			}
			binds = append(binds, editorBinds)
			if attBinds := attachmentHelpBinds(k, hasAttachments, deleting, m.isAgentBusy()); len(attBinds) > 0 {
				binds = append(binds, attBinds)
			}
		case uiFocusMain:
			binds = append(
				binds,
				[]key.Binding{
					k.Chat.UpDown,
					k.Chat.UpDownOneItem,
					k.Chat.PageUp,
					k.Chat.PageDown,
				},
				[]key.Binding{
					k.Chat.HalfPageUp,
					k.Chat.HalfPageDown,
					k.Chat.Home,
					k.Chat.End,
					k.Chat.EndFollow,
				},
				[]key.Binding{
					k.Chat.Copy,
					k.Chat.ClearHighlight,
				},
			)
		case uiFocusTasks:
			binds = append(
				binds,
				[]key.Binding{
					k.Chat.UpDown,
					k.Chat.Expand,
				},
				[]key.Binding{
					k.Chat.ClearHighlight,
					k.Chat.BackgroundTasks,
				},
			)
		}
	default:
		if m.session == nil {
			// no session selected
			binds = append(
				binds,
				[]key.Binding{
					k.Commands,
					k.Models,
					k.Sessions,
					k.Themes,
				},
			)
			if showEditorPalettes {
				binds[len(binds)-1] = append(binds[len(binds)-1], editorPrefixHints(k, true)...)
			}
			editorBinds := []key.Binding{
				k.Editor.Newline,
				k.Editor.MentionFile,
				k.Editor.OpenEditor,
				k.Editor.PasteText,
				k.Editor.SelectAll,
				k.Editor.CopySelection,
				k.Editor.CutSelection,
			}
			if m.currentModelSupportsImages() {
				editorBinds = append(editorBinds, k.Editor.AddImage, k.Editor.PasteImage)
			}
			binds = append(binds, editorBinds)
			if attBinds := attachmentHelpBinds(k, hasAttachments, deleting, m.isAgentBusy()); len(attBinds) > 0 {
				binds = append(binds, attBinds)
			}
		}
	}

	quit := k.Quit
	if m.isQuitting {
		quit.SetHelp(keys.HelpKeys(quit), "press again to quit")
	}
	binds = append(
		binds,
		[]key.Binding{
			help,
			quit,
		},
	)

	return binds
}

func (m *UI) currentModelSupportsImages() bool {
	cfg := m.com.Config()
	if cfg == nil {
		return false
	}
	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return false
	}
	model := cfg.GetModelByType(agentCfg.Model)
	return model != nil && model.SupportsImages
}

// updateLayoutAndSize updates the layout and sizes of UI components.
func (m *UI) updateLayoutAndSize() {
	// First pass sizes components from the current textarea height.
	m.layout = m.generateLayout(m.width, m.height)
	prevHeight := m.textarea.Height()
	m.updateSize()

	// SetWidth can change textarea height due to soft-wrap recalculation.
	// If that happens, run one reconciliation pass with the new height.
	if m.textarea.Height() != prevHeight {
		m.layout = m.generateLayout(m.width, m.height)
		m.updateSize()
	}
}

// handleTextareaHeightChange checks whether the textarea height changed and,
// if so, recalculates the layout. When the chat is in follow mode it keeps
// the view scrolled to the bottom. The returned command, if non-nil, must be
// batched by the caller.
func (m *UI) handleTextareaHeightChange(prevHeight int) tea.Cmd {
	if m.textarea.Height() == prevHeight {
		return nil
	}
	m.updateLayoutAndSize()
	if m.state == uiChat && m.chat.Follow() {
		m.chat.ScrollToBottom()
	}
	return nil
}

// updateTextarea updates the textarea for msg and then reconciles layout if
// the textarea height changed as a result.
func (m *UI) updateTextarea(msg tea.Msg) tea.Cmd {
	return m.updateTextareaWithPrevHeight(msg, m.textarea.Height())
}

// editorContentOrigin returns the top-left cell of the editor's
// content - the attachments strip, or without pills the textarea -
// directly below the top frame line. Together with textareaOrigin it
// is the single source for everything that translates between screen
// space and the editor's local space (cursor positioning, mouse
// forwarding, the completions popup, the strip's hit-testing), so none
// of them can drift from what drawEditorArea draws.
func (m *UI) editorContentOrigin() image.Point {
	return image.Pt(m.layout.editor.Min.X, m.layout.editor.Min.Y+1) // +1: top frame line
}

// textareaOrigin returns the textarea's top-left cell in screen space:
// the content origin, pushed down past the attachments strip when
// there is one.
func (m *UI) textareaOrigin() image.Point {
	origin := m.editorContentOrigin()
	if m.hasAttachments() {
		origin.Y += editorHeightMargin
	}
	return origin
}

// forwardMouseToTextarea forwards a mouse event to the textarea with
// coordinates translated into the textarea's local space. It reports whether
// the event landed within the textarea's rendered region and was forwarded.
func (m *UI) forwardMouseToTextarea(msg tea.MouseMsg) (bool, tea.Cmd) {
	mouse := msg.Mouse()

	// The textarea is rendered inside layout.editor below the
	// attachments strip when there is one.
	origin := m.textareaOrigin()

	// The textarea occupies its own height starting at the origin.
	area := image.Rectangle{Min: origin, Max: origin.Add(image.Pt(m.layout.editor.Dx(), m.textarea.Height()))}
	if !image.Pt(mouse.X, mouse.Y).In(area) {
		return false, nil
	}

	rel := tea.Mouse{
		X:      mouse.X - origin.X,
		Y:      mouse.Y - origin.Y,
		Button: mouse.Button,
		Mod:    mouse.Mod,
	}

	switch msg.(type) {
	case tea.MouseClickMsg:
		if rel.Button != uv.MouseLeft {
			return false, nil
		}
		m.textareaMouseSelecting = true
		m.textarea.BeginSelection(rel.X, rel.Y)
		return true, nil
	case tea.MouseMotionMsg:
		if !m.textareaMouseSelecting {
			return true, nil
		}
		m.textarea.ExtendSelection(rel.X, rel.Y)
		return true, nil
	case tea.MouseReleaseMsg:
		m.textarea.EndSelection()
		m.textareaMouseSelecting = false
		return true, nil
	default:
		return false, nil
	}
}

// updateTextareaWithPrevHeight is for cases when the height of the layout may
// have changed.
//
// Particularly, it's for cases where the textarea changes before
// textarea.Update is called (for example, SetValue, Reset, and InsertRune). We
// pass the height from before those changes took place so we can compare
// "before" vs "after" sizing and recalculate the layout if the textarea grew
// or shrank.
func (m *UI) updateTextareaWithPrevHeight(msg tea.Msg, prevHeight int) tea.Cmd {
	ta, cmd := m.textarea.Update(msg)
	m.textarea = ta
	return tea.Batch(cmd, m.handleTextareaHeightChange(prevHeight))
}

// updateSize updates the sizes of UI components based on the current layout.
func (m *UI) updateSize() {
	m.invalidateFrames()

	// Set status width
	m.status.SetWidth(m.layout.status.Dx())

	m.chat.SetSize(m.layout.main.Dx(), m.layout.main.Dy())
	m.textarea.MaxHeight = TextareaMaxHeight
	m.textarea.SetWidth(m.layout.editor.Dx())
	m.renderPills()
}

// splitOffEditor slices the compact status line and the prompt textarea off
// the bottom of area: one row for the status line with editorHeight rows of
// editor directly beneath it, so the two can never drift apart. It then
// expands both by sideMargin cells so they run flush to the screen edges -
// it must cancel exactly the side inset the caller's state applied to area
// (via its ancestor appRect), not a hardcoded guess, or the two drift apart
// exactly as they did when uiLanding's extra padding went uncancelled.
// Shared by every state with an editor so they can't diverge again.
func splitOffEditor(area image.Rectangle, editorHeight, sideMargin int) (rest, header, editor image.Rectangle) {
	layout.Vertical(
		layout.Len(area.Dy()-editorHeight-1),
		layout.Len(1),
		layout.Fill(1),
	).Split(area).Assign(&rest, &header, &editor)
	for _, r := range []*image.Rectangle{&header, &editor} {
		r.Min.X -= sideMargin
		r.Max.X += sideMargin
	}
	return rest, header, editor
}

// generateLayout calculates the layout rectangles for all UI components based
// on the current UI state and terminal dimensions.
func (m *UI) generateLayout(w, h int) uiLayout {
	// The screen area we're working with
	area := image.Rect(0, 0, w, h)

	// The help height
	helpHeight := 1
	// The editor height: its content (the textarea plus the
	// attachments strip while it has pills; an active inline editor's
	// height instead) wrapped in the frame lines drawn above and below
	// it.
	editorHeight := m.textarea.Height()
	if m.hasAttachments() {
		editorHeight += editorHeightMargin
	}
	if m.activeInline != nil {
		// The editor content width depends only on terminal width
		// and layout (not on editor height), so passing the current
		// frame's width to Height() keeps layout in sync with the
		// width Draw will use, preventing flicker during fast resize.
		editorWidth := m.editorContentWidth()
		if m.focus == uiFocusEditor {
			editorHeight = m.activeInline.Height(editorWidth)
		} else if qf, ok := m.activeInline.(*dialog.QuestionForm); ok && m.shouldCollapseQuestion(qf) {
			editorHeight = qf.CollapsedHeight() + 1
		} else {
			editorHeight = m.activeInline.Height(editorWidth)
		}
	}
	editorHeight += editorFrameRows
	// The header height
	const landingHeaderHeight = 0

	var helpKeyMap help.KeyMap = m
	if m.status != nil && m.status.ShowingAll() {
		for _, row := range helpKeyMap.FullHelp() {
			helpHeight = max(helpHeight, len(row))
		}
	}

	// Add app margins. The app area runs all the way down to the help
	// row: the editor and task strip sit directly above the hints, and
	// the transcript gets the row a separator would have wasted.
	var appRect, helpRect image.Rectangle
	layout.Vertical(
		layout.Len(area.Dy()-helpHeight),
		layout.Fill(1),
	).Split(area).Assign(&appRect, &helpRect)
	appRect.Min.Y += 1
	appRect.Min.X += 1
	appRect.Max.X -= 1

	// sideMargin tracks how many cells of left/right inset appRect now
	// carries, so splitOffEditor can cancel exactly that much rather than
	// a hardcoded amount that silently goes stale when a state's padding
	// changes here.
	sideMargin := 1
	if slices.Contains([]uiState{uiOnboarding, uiLanding}, m.state) {
		// extra padding on left and right for these states
		appRect.Min.X += 1
		appRect.Max.X -= 1
		sideMargin = 2
	}

	uiLayout := uiLayout{
		area:   area,
		status: helpRect,
	}

	// Handle different app states
	switch m.state {
	case uiOnboarding:
		// Layout
		//
		// header
		// ------
		// main
		// ------
		// help

		var headerRect, mainRect image.Rectangle
		layout.Vertical(
			layout.Len(landingHeaderHeight),
			layout.Fill(1),
		).Split(appRect).Assign(&headerRect, &mainRect)
		uiLayout.header = headerRect
		uiLayout.main = mainRect

	case uiLanding:
		// Layout
		//
		// main
		// ------
		// header (compact status line)
		// editor
		// ------
		// help
		mainRect, headerRect, editorRect := splitOffEditor(appRect, editorHeight, sideMargin)
		uiLayout.header = headerRect
		uiLayout.main = mainRect
		uiLayout.editor = editorRect

	case uiChat:
		// Layout
		//
		// main
		// ------
		// header (compact status line)
		// editor
		// ------
		// help
		mainRect, headerRect, editorRect := splitOffEditor(appRect, editorHeight, sideMargin)
		mainRect.Max.X -= 1 // Add padding right
		uiLayout.header = headerRect
		tasksHeight := m.tasksAreaHeight()
		if tasksHeight > 0 {
			tasksHeight = min(tasksHeight, mainRect.Dy())
			var chatRect, tasksRect image.Rectangle
			layout.Vertical(
				layout.Len(mainRect.Dy()-tasksHeight),
				layout.Fill(1),
			).Split(mainRect).Assign(&chatRect, &tasksRect)
			uiLayout.tasks = tasksRect
			mainRect = chatRect
		}
		pillsHeight := m.pillsAreaHeight()
		if pillsHeight > 0 {
			pillsHeight = min(pillsHeight, mainRect.Dy())
			var chatRect, pillsRect image.Rectangle
			layout.Vertical(
				layout.Len(mainRect.Dy()-pillsHeight),
				layout.Fill(1),
			).Split(mainRect).Assign(&chatRect, &pillsRect)
			uiLayout.main = chatRect
			uiLayout.pills = pillsRect
		} else {
			uiLayout.main = mainRect
		}
		// Add bottom margin to main
		uiLayout.main.Max.Y -= 1
		uiLayout.editor = editorRect
	}

	return uiLayout
}

// uiLayout defines the positioning of UI elements.
type uiLayout struct {
	// area is the overall available area.
	area uv.Rectangle

	// header is the compact status line, one row directly above the
	// editor in every state that has one. It is carved together with the
	// editor rect (splitOffEditor) so the two cannot drift apart.
	header uv.Rectangle

	// main is the area for the main pane. (e.g. chat, landing)
	main uv.Rectangle

	// pills is the area for the pills panel.
	pills uv.Rectangle

	// editor is the area for the editor pane.
	editor uv.Rectangle

	// tasks is the area for the background tasks strip (subagents).
	tasks uv.Rectangle

	// status is the area for the status view.
	status uv.Rectangle
}

func (m *UI) openEditor(value string) tea.Cmd {
	tmpfile, err := os.CreateTemp("", "msg_*.md")
	if err != nil {
		return util.ReportError(err)
	}
	tmpPath := tmpfile.Name()
	defer tmpfile.Close() //nolint:errcheck
	if _, err := tmpfile.WriteString(value); err != nil {
		return util.ReportError(err)
	}
	cmd, err := editor.Command(
		"harness",
		tmpPath,
		editor.AtPosition(
			m.textarea.Line()+1,
			m.textarea.Column()+1,
		),
	)
	if err != nil {
		return util.ReportError(err)
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer func() {
			_ = os.Remove(tmpPath)
		}()

		if err != nil {
			return util.ReportError(err)
		}
		content, err := os.ReadFile(tmpPath)
		if err != nil {
			return util.ReportError(err)
		}
		if len(content) == 0 {
			return util.ReportWarn("Message is empty")
		}
		return openEditorMsg{
			Text: strings.TrimSpace(string(content)),
		}
	})
}

// setEditorPrompt configures the textarea prompt function based on whether
// bang mode is enabled.
func (m *UI) setEditorPrompt() {
	if m.bangMode {
		m.textarea.SetPromptFunc(4, m.bangPromptFunc)
		return
	}
	m.textarea.SetPromptFunc(4, m.normalPromptFunc)
}

// normalPromptFunc returns the normal editor prompt style ("  ❯ " on first
// line, "::: " on subsequent lines).
func (m *UI) normalPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return "  ❯ "
		}
		return "::: "
	}
	if info.Focused {
		return t.Editor.PromptNormalFocused.Render()
	}
	return t.Editor.PromptNormalBlurred.Render()
}

// bangPromptFunc returns the bang mode editor prompt style with Turtle-colored
// icon and dots.
func (m *UI) bangPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return t.Editor.PromptBangIconFocused.Render()
		}
		return t.Editor.PromptBangIconBlurred.Render()
	}
	if info.Focused {
		return t.Editor.PromptBangDotsFocused.Render()
	}
	return t.Editor.PromptBangDotsBlurred.Render()
}

// closeCompletions closes the completions popup and resets state.
func (m *UI) closeCompletions() {
	m.completionsOpen = false
	m.completionsQuery = ""
	m.completionsStartIndex = 0
	m.completions.Close()
}

// insertCompletionText replaces the @query in the textarea with the given text.
// Returns false if the replacement cannot be performed.
func (m *UI) insertCompletionText(text string) bool {
	value := m.textarea.Value()
	if m.completionsStartIndex > len(value) {
		return false
	}

	word := m.textareaWord()
	endIdx := min(m.completionsStartIndex+len(word), len(value))
	newValue := value[:m.completionsStartIndex] + text + value[endIdx:]
	m.textarea.SetValue(newValue)
	m.textarea.MoveToEnd()
	m.textarea.InsertRune(' ')
	return true
}

// insertFileCompletion inserts the selected file path into the textarea,
// replacing the @query, and adds the file as an attachment.
func (m *UI) insertFileCompletion(path string) tea.Cmd {
	prevHeight := m.textarea.Height()
	if !m.insertCompletionText(path) {
		return nil
	}
	heightCmd := m.handleTextareaHeightChange(prevHeight)

	fileCmd := func() tea.Msg {
		if !m.currentModelSupportsImages() && common.IsImagePath(path) {
			return util.NewWarnMsg("The current model does not support image attachments")
		}

		absPath, _ := filepath.Abs(path)

		if m.hasSession() {
			// Skip attachment if file was already read and hasn't been modified.
			lastRead := m.com.Workspace.FileTrackerLastReadTime(context.Background(), m.session.ID, absPath)
			if !lastRead.IsZero() {
				if info, err := os.Stat(path); err == nil && !info.ModTime().After(lastRead) {
					return nil
				}
			}
		} else if slices.Contains(m.sessionFileReads, absPath) {
			return nil
		}

		m.sessionFileReads = append(m.sessionFileReads, absPath)

		// Add file as attachment.
		content, err := os.ReadFile(path)
		if err != nil {
			// If it fails, let the LLM handle it later.
			return nil
		}

		return message.Attachment{
			FilePath: path,
			FileName: filepath.Base(path),
			MimeType: mimeOf(content),
			Content:  content,
		}
	}
	return tea.Batch(heightCmd, fileCmd)
}

// insertSubagentCompletion inserts @name into the textarea, replacing the @query.
func (m *UI) insertSubagentCompletion(name string) tea.Cmd {
	prevHeight := m.textarea.Height()
	if !m.insertCompletionText("@" + name) {
		return nil
	}
	return m.handleTextareaHeightChange(prevHeight)
}

// insertMCPResourceCompletion inserts the selected resource into the textarea,
// replacing the @query, and adds the resource as an attachment.
func (m *UI) insertMCPResourceCompletion(item completions.ResourceCompletionValue) tea.Cmd {
	displayText := cmp.Or(item.Title, item.URI)

	prevHeight := m.textarea.Height()
	if !m.insertCompletionText(displayText) {
		return nil
	}
	heightCmd := m.handleTextareaHeightChange(prevHeight)

	resourceCmd := func() tea.Msg {
		contents, err := m.com.Workspace.ReadMCPResource(
			context.Background(),
			item.MCPName,
			item.URI,
		)
		if err != nil {
			slog.Warn("Failed to read MCP resource", "uri", item.URI, "error", err)
			return nil
		}
		if len(contents) == 0 {
			return nil
		}

		content := contents[0]
		var data []byte
		if content.Text != "" {
			data = []byte(content.Text)
		} else if len(content.Blob) > 0 {
			data = content.Blob
		}
		if len(data) == 0 {
			return nil
		}

		mimeType := item.MIMEType
		if mimeType == "" && content.MIMEType != "" {
			mimeType = content.MIMEType
		}
		if mimeType == "" {
			mimeType = "text/plain"
		}

		if !m.currentModelSupportsImages() && strings.HasPrefix(mimeType, "image/") {
			return util.NewWarnMsg("The current model does not support image attachments")
		}

		return message.Attachment{
			FilePath: item.URI,
			FileName: displayText,
			MimeType: mimeType,
			Content:  data,
		}
	}
	return tea.Batch(heightCmd, resourceCmd)
}

// completionsPosition returns the X and Y position for the completions popup.
func (m *UI) completionsPosition() image.Point {
	origin := m.textareaOrigin()
	if cur := m.textarea.Cursor(); cur != nil {
		origin.X += cur.X
		origin.Y += cur.Y
	}
	return origin
}

// textareaWord returns the current word at the cursor position.
func (m *UI) textareaWord() string {
	return m.textarea.Word()
}

// isWhitespace returns true if the byte is a whitespace character.
func isWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// isAgentBusy returns true if the agent coordinator exists and is currently
// busy processing a request. It only reads the memoized state (it runs in
// per-message paths like the textarea placeholder, where a workspace probe
// would be an HTTP round-trip per keystroke in client/server mode); the
// value is refreshed off-thread, see workspace_cache.go.
func (m *UI) isAgentBusy() bool {
	if m.bangCancel != nil {
		return true
	}
	return m.agentBusyCache.val
}

// hasSession returns true if there is an active session with a valid ID.
func (m *UI) hasSession() bool {
	return m.session != nil && m.session.ID != ""
}

// CurrentSession returns the active session, or nil when there is none.
// It is safe to call after the TUI has exited.
func (m *UI) CurrentSession() *session.Session {
	return m.session
}

// mimeOf detects the MIME type of the given content.
func mimeOf(content []byte) string {
	mimeBufferSize := min(512, len(content))
	return http.DetectContentType(content[:mimeBufferSize])
}

var readyPlaceholders = [...]string{
	"Ready!",
	"Ready...",
	"Ready?",
	"Ready for instructions",
}

var workingPlaceholders = [...]string{
	"Working!",
	"Working...",
	"Brrrrr...",
	"Prrrrrrrr...",
	"Processing...",
	"Thinking...",
}

// randomizePlaceholders selects random placeholder text for the textarea's
// ready and working states.
func (m *UI) randomizePlaceholders() {
	m.workingPlaceholder = workingPlaceholders[rand.Intn(len(workingPlaceholders))]
	m.readyPlaceholder = readyPlaceholders[rand.Intn(len(readyPlaceholders))]
}

// drawEditorArea draws whatever occupies the editor region - the
// active inline editor (or its collapsed form, when focus has moved
// away and it asks to collapse) if one is present, otherwise the
// prompt textarea and its attachments - framed by a rule line above
// and below. Shared by every state with an editor region so they draw
// it identically and can't drift apart; generateLayout reserves the
// frame rows (editorFrameRows) and editorContentOrigin derives the
// content's top from the same knowledge.
func (m *UI) drawEditorArea(scr uv.Screen, editorRect uv.Rectangle) {
	if editorRect.Dy() > editorFrameRows {
		frame := uv.NewStyledString(m.com.Styles.Editor.Frame.Render(
			strings.Repeat("─", editorRect.Dx())))
		frame.Draw(scr, image.Rect(editorRect.Min.X, editorRect.Min.Y, editorRect.Max.X, editorRect.Min.Y+1))
		frame.Draw(scr, image.Rect(editorRect.Min.X, editorRect.Max.Y-1, editorRect.Max.X, editorRect.Max.Y))
		content := editorRect
		content.Min.Y++
		content.Max.Y--
		editorRect = content
	}
	if m.activeInline != nil {
		m.activeInline.SetFocused(m.focus == uiFocusEditor)
		if m.focus == uiFocusEditor {
			m.inlineCursor = m.activeInline.Draw(scr, editorRect)
		} else if qf, ok := m.activeInline.(*dialog.QuestionForm); ok && m.shouldCollapseQuestion(qf) {
			qf.DrawCollapsed(scr, editorRect)
			m.inlineCursor = nil
		} else {
			m.inlineCursor = m.activeInline.Draw(scr, editorRect)
		}
		return
	}
	editor := uv.NewStyledString(m.renderEditorView(scr.Bounds().Dx()))
	editor.Draw(scr, editorRect)
	m.inlineCursor = nil
}

// hasAttachments reports whether the attachments strip has pills to
// show. The single source for the layout row reservation, the strip
// render, the mouse hit region and the help gating, so they cannot
// drift apart.
func (m *UI) hasAttachments() bool {
	return m.attachments != nil && len(m.attachments.List()) > 0
}

// renderEditorView renders the editor view with attachments if any.
// With no attachments the textarea is the whole area - no blank
// placeholder row is reserved.
func (m *UI) renderEditorView(width int) string {
	if m.hasAttachments() {
		return m.attachments.Render(width) + "\n" + m.textarea.View()
	}
	return m.textarea.View()
}

// applyThemeForProvider swaps the active theme to the one associated with
// the given provider, but only when that theme differs from the one
// already applied. Most providers share a single theme, so re-selecting a
// model from the same theme family would otherwise pay the full cost of
// invalidating the markdown renderer cache and re-rendering the entire
// transcript for no visible change.
func (m *UI) applyThemeForProvider(providerID string) {
	// A theme configured via options.tui.theme wins over the
	// provider-based mapping; switching providers must not swap it.
	if name := common.ThemeNameFromConfig(m.com.Config()); name != "" {
		m.themeKey = "config:" + strings.ToLower(name)
		return
	}
	key := styles.ThemeKeyForProvider(providerID)
	if key == m.themeKey {
		return
	}
	m.themeKey = key
	m.applyTheme(styles.ThemeForProvider(providerID))
}

// previewTheme applies a theme by name without persisting it, so the theme
// picker can show what an entry actually looks like across the whole UI.
// The first preview snapshots the current styles so [revertThemePreview]
// can put them back.
func (m *UI) previewTheme(name string) {
	m.themePreview.begin(*m.com.Styles, m.themeKey)
	if !m.themePreview.set(name) {
		return
	}
	m.themeKey = "config:" + strings.ToLower(name)
	m.applyTheme(styles.ThemeFromConfig(name))
}

// revertThemePreview restores the theme that was active before the theme
// picker started previewing. It is a no-op when nothing was previewed.
func (m *UI) revertThemePreview() {
	restore, restoreKey, ok := m.themePreview.take()
	if !ok {
		return
	}
	m.themeKey = restoreKey
	m.applyTheme(restore)
}

// commitThemePreview keeps the previewed theme, dropping the snapshot so a
// later close doesn't revert it.
func (m *UI) commitThemePreview() {
	m.themePreview.clear()
}

// applyTheme replaces the active styles with the given theme, drops the
// shared markdown renderer cache, and refreshes every component that
// caches style data.
func (m *UI) applyTheme(s styles.Styles) {
	*m.com.Styles = s
	common.InvalidateMarkdownRendererCache()
	m.refreshStyles()
}

// refreshStyles pushes the current *m.com.Styles into every subcomponent
// that copies or pre-renders style-dependent values at construction time.
func (m *UI) refreshStyles() {
	t := m.com.Styles
	m.header.refresh()
	m.textarea.SetStyles(t.Editor.Textarea)
	m.completions.SetStyles(t.Completions.Normal, t.Completions.Focused, t.Completions.Match)
	m.attachments.Renderer().SetStyles(
		t.Attachments.Normal,
		t.Attachments.Deleting,
		t.Attachments.Image,
		t.Attachments.Text,
		t.Attachments.Skill,
		t.Attachments.Remove,
	)
	m.todoSpinner.Style = t.Pills.TodoSpinner
	m.status.help.Styles = t.Help
	m.chat.InvalidateRenderCaches()
}

// attachSkill reads a skill's content by ID and returns it as a markdown
// attachment to be added to the attachment toolbar. The user can then
// compose a message and send it with the skill attached.
// The name parameter is used as a fallback when the server does not
// return one.
func (m *UI) attachSkill(skillID, name string) tea.Cmd {
	return func() tea.Msg {
		content, result, err := m.com.Workspace.ReadSkill(context.Background(), skillID)
		if err != nil {
			return util.NewErrorMsg(err)
		}
		fileName := result.Name
		if fileName == "" {
			fileName = name
		}
		return message.Attachment{
			FilePath: fileName,
			FileName: fileName,
			MimeType: "text/markdown",
			Content:  content,
		}
	}
}

// sendMessage sends a message with the given content and attachments.
func (m *UI) sendMessage(content string, attachments ...message.Attachment) tea.Cmd {
	content = rewriteSubagentPrompt(content, m.activeSubagentNames)

	if err := m.com.Workspace.AgentReadyErr(); err != nil {
		return util.ReportError(err)
	}

	// Start the turn timer.
	common.StartTurn()

	var cmds []tea.Cmd
	if !m.hasSession() {
		newSession, err := m.com.Workspace.CreateSession(context.Background(), "New Session")
		if err != nil {
			return util.ReportError(err)
		}
		if newSession.ID != "" {
			m.session = &newSession
			cmds = append(cmds, m.loadSession(newSession.ID))
		}
		m.setState(uiChat, m.focus)
	}

	ctx := context.Background()
	cmds = append(cmds, func() tea.Msg {
		for _, path := range m.sessionFileReads {
			m.com.Workspace.FileTrackerRecordRead(ctx, m.session.ID, path)
			m.com.Workspace.LSPStart(ctx, path)
		}
		return nil
	})

	// Capture session ID to avoid race with main goroutine updating m.session.
	sessionID := m.session.ID
	// Capture the pre-send state: a prompt submitted while the agent is
	// busy (or behind an existing queue) is enqueued server-side, so show
	// it in the transcript right away as a queued placeholder.
	willQueue := m.isAgentBusy() || m.promptQueue > 0
	// Optimistically mark the agent busy: the prompt we are about to submit
	// either starts a run or is enqueued behind one. This keeps esc pressed
	// right after enter routing to cancelAgent instead of reading a stale
	// idle value; the authoritative state arrives via agentRunSubmittedMsg.
	// Bump the busy/queue generations so any probe started before this
	// optimistic write is discarded rather than reverting us to idle.
	m.agentBusyCache.set(true)
	m.busyFetchGen++
	m.invalidatePromptQueue()
	if willQueue {
		m.appendQueuedPrompt(content)
	}
	// A new turn supersedes any lingering retry notice.
	m.clearRetryNotice()
	cmds = append(cmds, func() tea.Msg {
		// AgentRun is fire-and-forget: it returns once the prompt has
		// been accepted (HTTP 202) or synchronously with a validation
		// or transport error. Run failures and cancellation surface
		// through SSE-derived events, not this return value.
		err := m.com.Workspace.AgentRun(context.Background(), sessionID, content, attachments...)
		if err != nil && !errors.Is(err, context.Canceled) {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("%v", err),
			}
		}
		return agentRunSubmittedMsg{}
	})
	return tea.Batch(cmds...)
}

// runShellCommand executes a shell command server-side without triggering
// the LLM. The result is displayed as a tool-style item in the chat.
func (m *UI) runShellCommand(command string) tea.Cmd {
	return m.runShellCommandInternal(command, false)
}

// runShellCommandInternal is the shared implementation for bang-mode shell
// execution. isFirstMessage indicates the command is the first user message
// in a newly created session, which triggers title generation.
func (m *UI) runShellCommandInternal(command string, isFirstMessage bool) tea.Cmd {
	var cmds []tea.Cmd
	if !m.hasSession() {
		newSession, err := m.com.Workspace.CreateSession(context.Background(), "New Session")
		if err != nil {
			return util.ReportError(err)
		}
		if newSession.ID != "" {
			m.session = &newSession
			cmds = append(cmds, m.loadSession(newSession.ID))
		}
		m.setState(uiChat, m.focus)
		// Defer shell execution until loadSessionMsg fires so the chat
		// list is stable before we add items or start streaming.
		m.pendingBangCommand = command
		return tea.Batch(cmds...)
	}

	sessionID := m.session.ID
	contentWidth := min(m.layout.main.Dx()-2, 120)

	// Append a pending shell item immediately so the user sees feedback.
	pendingItem := chat.NewPendingShellItem(m.com.Styles, command)
	// Bang mode runs without the agent, so re-enable the animation clock
	// that a non-busy session reload may have frozen.
	m.chat.SetAnimationsAllowed(true)
	m.chat.AppendMessages(pendingItem)
	m.chat.ScrollToBottom()

	// Stream output via channel. The progress callback writes chunks
	// to streamCh; a reader cmd converts them to shellStreamMsg values.
	streamCh := make(chan string, 64)
	pendingID := pendingItem.ID()

	onProgress := func(chunk string) {
		select {
		case streamCh <- chunk:
		default:
			// Drop if UI can't keep up.
		}
	}

	// Reader cmd: drains streamCh into shellStreamMsg until closed.
	cmds = append(cmds, func() tea.Msg {
		chunk, ok := <-streamCh
		if !ok {
			return nil
		}
		return shellStreamMsg{PendingID: pendingID, Chunk: chunk, streamCh: streamCh}
	})

	ctx, cancel := context.WithCancel(context.Background())
	m.bangCancel = cancel

	cmds = append(cmds, func() tea.Msg {
		resp, err := m.com.Workspace.AgentRunShellCommand(ctx, sessionID, command, contentWidth, onProgress, isFirstMessage)
		close(streamCh)
		if err != nil && !errors.Is(err, context.Canceled) {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("shell: %v", err),
			}
		}
		exitCode := resp.ExitCode
		if errors.Is(err, context.Canceled) {
			exitCode = 130 // conventional SIGINT exit code
		}
		return shellResultMsg{
			PendingID: pendingID,
			Command:   command,
			Output:    resp.Output,
			ExitCode:  exitCode,
		}
	})
	return tea.Batch(cmds...)
}

const (
	cancelTimerDuration = 2 * time.Second
	quitTimerDuration   = 1 * time.Second
)

// cancelTimerCmd creates a command that expires the cancel timer.
func cancelTimerCmd() tea.Cmd {
	return tea.Tick(cancelTimerDuration, func(time.Time) tea.Msg {
		return cancelTimerExpiredMsg{}
	})
}

// quitTimerCmd creates a command that expires the quit timer.
func quitTimerCmd() tea.Cmd {
	return tea.Tick(quitTimerDuration, func(time.Time) tea.Msg {
		return quitTimerExpiredMsg{}
	})
}

// quit handles the quit key press: the first press arms a short window and
// hints the user; a second press within the window quits the application
// without a confirmation dialog.
func (m *UI) quit() tea.Cmd {
	if m.isQuitting {
		m.isQuitting = false
		return tea.Quit
	}

	m.isQuitting = true
	keyHint := "ctrl+c"
	if keys := m.keyMap.Quit.Keys(); len(keys) > 0 {
		keyHint = keys[0]
	}
	return tea.Batch(
		util.ReportWarn("Press "+keyHint+" again to quit"),
		quitTimerCmd(),
	)
}

// cancelAgent handles the cancel key press while the agent is busy. The
// first press sets isCanceling to true and starts a timer. The second
// press (before the timer expires) interrupts the running turn — the
// turn only: queued prompts survive it (they stay rendered in the
// transcript and run once the interrupted turn unwinds), so escape never
// cancels the message.
func (m *UI) cancelAgent() tea.Cmd {
	if !m.hasSession() {
		return nil
	}

	// Gate on the memoized ready state: esc is a hot key and AgentIsReady
	// is a synchronous HTTP round-trip in client/server mode.
	if !m.agentReady {
		return nil
	}

	if m.isCanceling {
		// Second escape press — interrupt the running turn.
		m.isCanceling = false
		m.rewindEscArmed = false

		// Cancel a running bang command if one is in progress.
		if m.bangCancel != nil {
			m.bangCancel()
			m.bangCancel = nil
		}

		m.com.Workspace.AgentCancelTurn(m.session.ID)
		// Stop the spinning todo indicator and drop the memoized busy
		// state the cancel just changed; the pill re-renders now from
		// last-known state and again when the off-thread refresh (and
		// the agent's own events) land. The queued-prompt cache stays:
		// a turn-only cancel keeps the queue.
		m.todoIsSpinning = false
		m.invalidateBusyCaches()
		m.renderPills()
		return m.dispatchBusyRefresh()
	}

	// First escape press - set canceling state and start timer.
	m.isCanceling = true
	m.rewindEscArmed = false
	return cancelTimerCmd()
}

// handleRewindEscape implements the idle half of the escape contract:
// with no LLM action running, a double escape opens the rewind picker —
// rewinding to the newest turn cancels the last message and restores
// its text into the editor. It reports whether it consumed the press; a
// draft, open completions, or history browsing own the press instead,
// and while the agent is busy escape stays the turn cancel.
func (m *UI) handleRewindEscape() (bool, tea.Cmd) {
	if m.state != uiChat || m.focus != uiFocusEditor || m.isAgentBusy() || !m.hasSession() {
		m.rewindEscArmed = false
		return false, nil
	}
	// A draft, open completions, or history browsing own the press. The
	// messages-length guard keeps the zero value of index (0) from
	// reading as "browsing" before any history has loaded.
	if m.completionsOpen || (m.promptHistory.index >= 0 && len(m.promptHistory.messages) > 0) || m.textarea.Value() != "" {
		m.rewindEscArmed = false
		return false, nil
	}
	if m.rewindEscArmed {
		m.rewindEscArmed = false
		m.isCanceling = false
		return true, m.openRewindDialog()
	}
	m.rewindEscArmed = true
	m.isCanceling = false
	return true, cancelTimerCmd()
}

// openDialog opens a dialog by its ID.
func (m *UI) openDialog(id string) tea.Cmd {
	var cmds []tea.Cmd
	switch id {
	case dialog.SessionsID:
		if cmd := m.openSessionsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ModelsID:
		if cmd := m.openModelsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.CommandsID:
		if cmd := m.openCommandsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ReasoningID:
		if cmd := m.openReasoningDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ConnectID:
		if cmd := m.openConnectDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ThemesID:
		if cmd := m.openThemesDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.RewindID:
		if cmd := m.openRewindDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.NotificationsID:
		if cmd := m.openNotificationsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.FilePickerID:
		if cmd := m.openFilesDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.MCPServersID:
		if cmd := m.openMCPServersDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.LSPServersID:
		if cmd := m.openLSPServersDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	default:
		// Unknown dialog
		break
	}
	return tea.Batch(cmds...)
}

// openRewindDialog opens the rewind picker over the session's user
// turns. Rewinding mid-run would race the tools still writing, so a
// busy agent refuses entry.
func (m *UI) openRewindDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.RewindID) {
		m.dialog.BringToFront(dialog.RewindID)
		return nil
	}
	if m.session == nil {
		return nil
	}
	if m.isAgentBusy() {
		return util.ReportWarn("Agent is working, please wait...")
	}
	rewind, err := dialog.NewRewind(m.com, m.session.ID)
	if err != nil {
		return util.ReportError(err)
	}
	m.dialog.OpenDialog(rewind)
	return nil
}

// openModelsDialog opens the models dialog.
func (m *UI) openModelsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ModelsID) {
		// Bring to front
		m.dialog.BringToFront(dialog.ModelsID)
		return nil
	}

	isOnboarding := m.state == uiOnboarding
	modelsDialog, err := dialog.NewModels(m.com, isOnboarding)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(modelsDialog)

	return nil
}

// openConnectDialog opens the provider connection dialog. An open models
// dialog gives way to it: the two are the same list seen from either side
// of having credentials.
func (m *UI) openConnectDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ConnectID) {
		m.dialog.BringToFront(dialog.ConnectID)
		return nil
	}

	connectDialog, err := dialog.NewConnect(m.com)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.CloseDialog(dialog.ModelsID)
	m.dialog.OpenDialog(connectDialog)
	return nil
}

// openCommandsDialog opens the commands dialog.
func (m *UI) openCommandsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.CommandsID) {
		// Bring to front
		m.dialog.BringToFront(dialog.CommandsID)
		return nil
	}

	var sessionID string
	hasSession := m.session != nil
	if hasSession {
		sessionID = m.session.ID
	}
	hasTodos := hasSession && hasIncompleteTodos(m.session.Todos)
	hasSummary := hasSession && m.session.SummaryMessageID != ""

	commands, err := dialog.NewCommands(m.com, sessionID, hasSession, hasSummary, hasTodos, m.customCommands, m.mcpPrompts)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(commands)

	return commands.InitialCmd()
}

// openSkillsDialog opens the skills palette, the "/" counterpart of
// the commands palette.
func (m *UI) openSkillsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.CommandsID) {
		// Already the skills palette: bring it to front. The commands
		// palette is swapped for the skills one instead.
		if front := m.dialog.DialogLast(); front != nil {
			if c, ok := front.(*dialog.Commands); ok && c.SkillsOnly() {
				m.dialog.BringToFront(dialog.CommandsID)
				return nil
			}
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	}

	skillsDialog, err := dialog.NewSkills(m.com, m.customCommands)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(skillsDialog)

	return skillsDialog.InitialCmd()
}

// openReasoningDialog opens the reasoning effort dialog.
func (m *UI) openReasoningDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ReasoningID) {
		m.dialog.BringToFront(dialog.ReasoningID)
		return nil
	}

	reasoningDialog, err := dialog.NewReasoning(m.com)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(reasoningDialog)
	return nil
}

// openThemesDialog opens the color theme picker.
func (m *UI) openThemesDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ThemesID) {
		m.dialog.BringToFront(dialog.ThemesID)
		return nil
	}

	m.dialog.OpenDialog(dialog.NewThemes(m.com))
	return nil
}

// openNotificationsDialog opens the notification style picker dialog.
func (m *UI) openNotificationsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.NotificationsID) {
		m.dialog.BringToFront(dialog.NotificationsID)
		return nil
	}

	notificationsDialog := dialog.NewNotifications(m.com)
	m.dialog.OpenDialog(notificationsDialog)
	return nil
}

// openSessionsDialog opens the sessions dialog. If the dialog is already open,
// it brings it to the front. Otherwise, it will list all the sessions and open
// the dialog.
func (m *UI) openSessionsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.SessionsID) {
		// Bring to front
		m.dialog.BringToFront(dialog.SessionsID)
		return nil
	}

	selectedSessionID := ""
	if m.session != nil {
		selectedSessionID = m.session.ID
	}

	dialog, err := dialog.NewSessions(m.com, selectedSessionID)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(dialog)
	return nil
}

// openFilesDialog opens the file picker dialog.
func (m *UI) openFilesDialog() tea.Cmd {
	if !m.currentModelSupportsImages() {
		return util.ReportWarn("The current model does not support image attachments")
	}
	if m.dialog.ContainsDialog(dialog.FilePickerID) {
		// Bring to front
		m.dialog.BringToFront(dialog.FilePickerID)
		return nil
	}

	filePicker, cmd := dialog.NewFilePicker(m.com)
	filePicker.SetImageCapabilities(&m.caps)
	m.dialog.OpenDialog(filePicker)
	event.FilePickerOpened()

	return cmd
}

// openBatchFormDialog activates a tabbed multi-question form in
// the editor area. Single questions render without tabs or confirm.
func (m *UI) openBatchFormDialog(batch question.Request) {
	// Close any existing question form first to prevent stacking.
	if qf, ok := m.activeInline.(*dialog.QuestionForm); ok && qf != nil {
		m.activeInline = nil
	}

	form := dialog.NewQuestionForm(m.com.Styles, batch)
	form.OnAnswer = func(responses []question.Answer) {
		m.com.Workspace.QuestionAnswer(responses)
	}
	form.OnCancel = func() {
		m.com.Workspace.QuestionCancel()
	}
	m.activeInline = form
	m.focusEditor()
	m.updateLayoutAndSize()
}

// handleQuestionNotification dismisses an open question form when
// any client resolved the pending batch. Only one question can be
// pending at a time, so any notification means the current form
// is stale regardless of BatchID. Returns the command that restarts
// the editor caret's blink cycle.
func (m *UI) handleQuestionNotification(_ question.Notification) tea.Cmd {
	if _, ok := m.activeInline.(*dialog.QuestionForm); ok {
		m.activeInline = nil
		cmd := m.focusEditor()
		m.updateLayoutAndSize()
		return cmd
	}
	return nil
}

// editorContentWidth returns the content width available to the
// editor area for the current state. It depends only on terminal
// width and layout (not on editor height), so it can be computed
// before the editor's height is known. This is the single source
// of truth for the inline editor width used by both layout sizing
// and Height() queries.
func (m *UI) editorContentWidth() int {
	return m.width - 2 // appRect horizontal margins
}

// shouldCollapseQuestion reports whether a question form should render
// in its collapsed one-line view. This is true only when the form is
// unfocused and would consume more than half the terminal height.
func (m *UI) shouldCollapseQuestion(qf *dialog.QuestionForm) bool {
	return m.focus != uiFocusEditor && m.height > 0 && qf.Height(m.editorContentWidth()) > m.height*2/5
}

// handleAgentNotification translates domain agent events into desktop
// notifications using the UI notification backend.
// clearRetryNotice drops a lingering provider-retry status note, if
// any. Callers are progress points (message traffic, new turn,
// session switch) proving the backoff is over.
func (m *UI) clearRetryNotice() {
	if !m.retryNotice {
		return
	}
	m.retryNotice = false
	m.status.ClearInfoMsg()
}

func (m *UI) handleAgentNotification(n notify.Notification) tea.Cmd {
	var cmds []tea.Cmd
	switch n.Type {
	case notify.TypeAgentFinished:
		common.StopTurn()
		cmds = append(cmds, m.sendNotification(notification.Notification{
			Title:   "Agent is waiting...",
			Message: fmt.Sprintf("Agent's turn completed in \"%s\"", n.SessionTitle),
		}))
	case notify.TypeAgentError:
		// Terminal edge like TypeAgentFinished; fall through to the
		// busy/queue refresh below. The toast is the single
		// user-visible signal for a failed turn (retries, if any,
		// were status-bar only), so an away user learns the run
		// actually failed instead of finding a silent error later.
		cmds = append(cmds, m.sendNotification(notification.Notification{
			Title:   "Agent run failed",
			Message: n.Message,
		}))
	case notify.TypeAgentRetrying:
		// Transient edge, not terminal: the run is still in flight
		// through its backoff. Pin a persistent status-bar note and
		// never touch the busy or queue caches, or ESC would observe
		// a phantom idle turn. Deliberately no toast here: retries
		// are routine and would spam the desktop; the single toast
		// fires on terminal failure (TypeAgentError) instead.
		m.retryNotice = true
		m.status.SetInfoMsg(util.InfoMsg{
			Type: util.InfoTypeWarn,
			Msg:  "Retrying provider request: " + n.Message,
		})
		return nil
	case notify.TypeReAuthenticate:
		return m.handleReAuthenticate(n.ProviderID)
	case notify.TypeAWSSSOAuth:
		return m.handleAWSSSOAuth(n.AWSSOCommand, n.AWSSOURL)
	case notify.TypeAWSSSOAuthResult:
		return m.handleAWSSSOAuthResult(n.Message)
	default:
		return nil
	}
	// TypeAgentFinished / TypeAgentError are the busy→idle edge: the agent
	// clears its active request before publishing precisely so observers
	// can re-probe. Drop the memoized busy state and re-fetch it and the
	// prompt queue off-thread.
	m.invalidateBusyCaches()
	m.invalidatePromptQueue()
	if cmd := m.dispatchBusyRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (m *UI) handleReAuthenticate(providerID string) tea.Cmd {
	cfg := m.com.Config()
	if cfg == nil {
		return nil
	}
	providerCfg, ok := cfg.Providers.Get(providerID)
	if !ok {
		return nil
	}
	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return nil
	}
	return m.openAuthenticationDialog(providerCfg.ToProvider(), cfg.Models[agentCfg.Model], agentCfg.Model)
}

// handleAWSSSOAuth opens the AWS SSO progress dialog (or updates the SSO URL
// on an already-open one). The refresh command runs in the coordinator; this
// dialog is a display surface driven by agent notifications.
func (m *UI) handleAWSSSOAuth(command, url string) tea.Cmd {
	// Update the URL on an already-open dialog.
	if existing := m.dialog.Dialog(dialog.AWSSSOID); existing != nil {
		if awsDlg, ok := existing.(*dialog.AWSSSO); ok && url != "" {
			awsDlg.SetURL(url)
		}
		m.dialog.BringToFront(dialog.AWSSSOID)
		return nil
	}
	if command == "" {
		return nil
	}
	dlg, cmd := dialog.NewAWSSSO(m.com, command)
	if url != "" {
		dlg.SetURL(url)
	}
	m.dialog.OpenDialogWithGrace(dlg)
	return cmd
}

// handleAWSSSOAuthResult finishes the AWS SSO dialog once the refresh command
// exits: it closes on success or shows the error so the user can dismiss it.
func (m *UI) handleAWSSSOAuthResult(errMsg string) tea.Cmd {
	existing := m.dialog.Dialog(dialog.AWSSSOID)
	if existing == nil {
		return nil
	}
	awsDlg, ok := existing.(*dialog.AWSSSO)
	if !ok {
		return nil
	}
	if errMsg == "" {
		// Success: the turn retries transparently, so no need to linger.
		m.dialog.CloseDialog(dialog.AWSSSOID)
		return nil
	}
	awsDlg.Finish(errMsg)
	return nil
}

// newSession clears the current session state and prepares for a new session.
// The actual session creation happens when the user sends their first message.
// Returns a command to reload prompt history.
func (m *UI) newSession() tea.Cmd {
	if !m.hasSession() {
		return nil
	}

	m.session = nil
	m.sessionFiles = nil
	m.sessionFileReads = nil
	m.knownChildSessionIDs = nil
	// Same reset the loadSessionMsg handler performs on a session switch. A new
	// session has no parent and no children, so leaving these set would keep
	// rendering the previous session's parent breadcrumb and "Active subagents"
	// panel on an empty screen until an unrelated event happened to clear them.
	m.runningSubagents = nil
	m.resetAgentTasks()
	m.parentTitle = ""
	m.subagentColor = ""
	m.setState(uiLanding, uiFocusEditor)
	cmd := m.focusEditor()
	m.chat.ClearMessages()
	m.pillsExpanded = false
	m.pillsAutoExpanded = false
	m.promptQueue = 0
	m.promptQueueItems = nil
	m.promptQueueCheckedAt = time.Now()
	m.invalidateBusyCaches()
	m.invalidatePromptQueue()
	m.pillsView = ""
	m.historyReset()
	agenttools.ResetCache()
	return tea.Batch(
		func() tea.Msg {
			m.com.Workspace.LSPStopAll(context.Background())
			return nil
		},
		m.loadPromptHistory(),
		m.reportCurrentSession(""),
		cmd,
	)
}

// saveSummaryToFile writes the session's latest summary message to a
// markdown file inside the data directory so it can be reused later,
// e.g. as the initial context of a new session, and copies it to the
// clipboard.
func (m *UI) saveSummaryToFile(sessionID string) tea.Cmd {
	return func() tea.Msg {
		sess, err := m.com.Workspace.GetSession(context.Background(), sessionID)
		if err != nil {
			return util.ReportError(err)()
		}
		msgs, err := m.com.Workspace.ListMessages(context.Background(), sessionID)
		if err != nil {
			return util.ReportError(err)()
		}
		path, content, err := saveSummaryExport(m.com.Config().Options.DataDirectory, sess, msgs)
		if err != nil {
			return util.ReportError(err)()
		}
		return reportExportCopied("Summary saved to", path, content)
	}
}

// reportExportCopied copies an export's text to the clipboard and reports
// where the file landed. The file is the export and the clipboard copy comes
// on top of it, so a clipboard that accepts the write and then does not hold
// the text still leaves the path on screen.
func reportExportCopied(label, path, content string) tea.Msg {
	return tea.Sequence(
		tea.SetClipboard(content),
		func() tea.Msg {
			msg := label + " " + path
			if err := clipboard.WriteText(content); errors.Is(err, clipboard.ErrWriteFailed) {
				msg += " (clipboard copy failed)"
			} else {
				msg += " and copied to clipboard"
			}
			return util.CmdHandler(util.InfoMsg{
				Type: util.InfoTypeSuccess,
				Msg:  msg,
			})()
		},
	)()
}

// exportConversationToFile writes the full transcript of the given session
// to a markdown file inside the data directory and copies the same markdown
// to the clipboard.
func (m *UI) exportConversationToFile(sessionID string) tea.Cmd {
	return func() tea.Msg {
		sess, err := m.com.Workspace.GetSession(context.Background(), sessionID)
		if err != nil {
			return util.ReportError(err)()
		}
		msgs, err := m.com.Workspace.ListMessages(context.Background(), sessionID)
		if err != nil {
			return util.ReportError(err)()
		}
		path, content, err := saveConversationExport(m.com.Config().Options.DataDirectory, sess, msgs)
		if err != nil {
			return util.ReportError(err)()
		}
		return reportExportCopied("Conversation exported to", path, content)
	}
}

// checkBangModeAfterPaste engages bang mode when pasted text starts with
// optional whitespace followed by "!". It strips the prefix and adjusts
// the cursor, mirroring the keypress bang-mode entry logic.
func (m *UI) checkBangModeAfterPaste() {
	if m.bangMode {
		return
	}
	val := m.textarea.Value()
	trimmed := strings.TrimLeftFunc(val, unicode.IsSpace)
	if !strings.HasPrefix(trimmed, "!") {
		return
	}
	m.bangMode = true
	m.bangWasEmpty = true
	stripped := trimmed[1:]
	m.textarea.SetValue(stripped)
	col := m.textarea.Column()
	m.textarea.SetCursorColumn(max(0, col-(len(val)-len(stripped))))
	m.setEditorPrompt()
}

// handlePasteMsg handles a paste message.
func (m *UI) handlePasteMsg(msg tea.PasteMsg) tea.Cmd {
	// Normalize \r\n before the textarea sanitizer sees it.
	msg.Content = strings.ReplaceAll(msg.Content, "\r\n", "\n")

	if m.dialog.HasDialogs() {
		return m.handleDialogMsg(msg)
	}

	if m.focus != uiFocusEditor {
		return nil
	}

	if hasPasteExceededThreshold(msg) {
		return func() tea.Msg {
			content := []byte(msg.Content)
			if int64(len(content)) > common.MaxAttachmentSize {
				return util.ReportWarn("Paste is too big (>5mb)")
			}
			name := fmt.Sprintf("paste_%d.txt", m.pasteIdx())
			mimeBufferSize := min(512, len(content))
			mimeType := http.DetectContentType(content[:mimeBufferSize])
			return pastedAttachmentMsg{
				attachment: message.Attachment{
					FileName: name,
					FilePath: name,
					MimeType: mimeType,
					Content:  content,
				},
				lines: strings.Count(msg.Content, "\n") + 1,
			}
		}
	}

	// Attempt to parse pasted content as file paths. If possible to parse,
	// all files exist and are valid, add as attachments.
	// Otherwise, paste as text.
	paths := fsext.ParsePastedFiles(msg.Content)
	allExistsAndValid := func() bool {
		if len(paths) == 0 {
			return false
		}
		for _, path := range paths {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return false
			}
			if !common.IsImagePath(path) {
				return false
			}
		}
		return true
	}
	if !allExistsAndValid() {
		prevHeight := m.textarea.Height()
		cmd := m.updateTextareaWithPrevHeight(msg, prevHeight)
		m.checkBangModeAfterPaste()
		return cmd
	}
	if !m.currentModelSupportsImages() {
		return util.ReportWarn("The current model does not support image attachments")
	}

	var cmds []tea.Cmd
	for _, path := range paths {
		cmds = append(cmds, m.handleFilePathPaste(path))
	}
	return tea.Batch(cmds...)
}

func hasPasteExceededThreshold(msg tea.PasteMsg) bool {
	var (
		lineCount = 0
		colCount  = 0
	)
	for line := range strings.SplitSeq(msg.Content, "\n") {
		lineCount++
		colCount = max(colCount, len(line))

		if lineCount > pasteLinesThreshold || colCount > pasteColsThreshold {
			return true
		}
	}
	return false
}

// handleFilePathPaste handles a pasted file path.
func (m *UI) handleFilePathPaste(path string) tea.Cmd {
	return func() tea.Msg {
		fileInfo, err := os.Stat(path)
		if err != nil {
			return util.ReportError(err)
		}
		if fileInfo.IsDir() {
			return util.ReportWarn("Cannot attach a directory")
		}
		if fileInfo.Size() > common.MaxAttachmentSize {
			return util.ReportWarn("File is too big (>5mb)")
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return util.ReportError(err)
		}

		mimeBufferSize := min(512, len(content))
		mimeType := http.DetectContentType(content[:mimeBufferSize])
		fileName := filepath.Base(path)
		return pastedAttachmentMsg{
			attachment: message.Attachment{
				FilePath: path,
				FileName: fileName,
				MimeType: mimeType,
				Content:  content,
			},
		}
	}
}

// pasteTextFromClipboard reads text from the system clipboard and returns a
// tea.PasteMsg so it flows through the same paste logic as bracketed paste.
func (m *UI) pasteTextFromClipboard() tea.Msg {
	textData, err := clipboard.Read(clipboard.FormatText)
	if err != nil || len(textData) == 0 {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  "Clipboard is empty or does not contain text",
		}
	}
	return tea.PasteMsg{Content: string(textData)}
}

// pasteImageFromClipboard reads image data from the system clipboard and
// creates an attachment. If no image data is found, it falls back to
// interpreting clipboard text as a file path.
func (m *UI) pasteImageFromClipboard() tea.Msg {
	if !m.currentModelSupportsImages() {
		return util.NewWarnMsg("The current model does not support image attachments")
	}
	imageData, err := clipboard.Read(clipboard.FormatImage)
	if int64(len(imageData)) > common.MaxAttachmentSize {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  "File too large, max 5MB",
		}
	}
	name := fmt.Sprintf("paste_%d.png", m.pasteIdx())
	if err == nil {
		return pastedAttachmentMsg{
			attachment: message.Attachment{
				FilePath: name,
				FileName: name,
				MimeType: mimeOf(imageData),
				Content:  imageData,
			},
		}
	}

	textData, textErr := clipboard.Read(clipboard.FormatText)
	if textErr != nil || len(textData) == 0 {
		return nil // Clipboard is empty or does not contain an image
	}

	path := strings.TrimSpace(string(textData))
	path = strings.ReplaceAll(path, "\\ ", " ")
	if _, statErr := os.Stat(path); statErr != nil {
		return nil // Clipboard does not contain an image or valid file path
	}

	if !common.IsImagePath(path) {
		return util.NewInfoMsg("File type is not a supported image format")
	}

	fileInfo, statErr := os.Stat(path)
	if statErr != nil {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  fmt.Sprintf("Unable to read file: %v", statErr),
		}
	}
	if fileInfo.Size() > common.MaxAttachmentSize {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  "File too large, max 5MB",
		}
	}

	content, readErr := os.ReadFile(path)
	if readErr != nil {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  fmt.Sprintf("Unable to read file: %v", readErr),
		}
	}

	return pastedAttachmentMsg{
		attachment: message.Attachment{
			FilePath: path,
			FileName: filepath.Base(path),
			MimeType: mimeOf(content),
			Content:  content,
		},
	}
}

var pasteRE = regexp.MustCompile(`paste_(\d+).txt`)

func (m *UI) pasteIdx() int {
	result := 0
	note := func(fileName string) {
		found := pasteRE.FindStringSubmatch(fileName)
		if len(found) == 0 {
			return
		}
		if idx, err := strconv.Atoi(found[1]); err == nil {
			result = max(result, idx)
		}
	}
	// Pastes live inline in the editor now, but names minted before a
	// send can still sit in either store; scan both so numbering never
	// collides.
	for _, at := range m.attachments.List() {
		note(at.FileName)
	}
	for _, at := range m.pastedAttachments {
		note(at.FileName)
	}
	return result + 1
}

func (m *UI) runMCPPrompt(clientID, promptID string, arguments map[string]string) tea.Cmd {
	load := func() tea.Msg {
		prompt, err := m.com.Workspace.GetMCPPrompt(clientID, promptID, arguments)
		if err != nil {
			// TODO: make this better
			return util.ReportError(err)()
		}

		if prompt == "" {
			return nil
		}
		return sendMessageMsg{
			Content: prompt,
		}
	}

	var cmds []tea.Cmd
	if cmd := m.dialog.StartLoading(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	cmds = append(cmds, load, func() tea.Msg {
		return closeDialogMsg{}
	})

	return tea.Sequence(cmds...)
}

func (m *UI) handleStateChanged() tea.Cmd {
	return m.updateAgentModelCmd(func() tea.Msg {
		m.com.Workspace.UpdateAgentModel(context.Background())
		return mcpStateChangedMsg{
			states: m.com.Workspace.MCPGetStates(),
		}
	})
}

func handleMCPPromptsEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.MCPRefreshPrompts(context.Background(), name)
		return nil
	}
}

func handleMCPToolsEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.RefreshMCPTools(context.Background(), name)
		return nil
	}
}

func handleMCPResourcesEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.MCPRefreshResources(context.Background(), name)
		return nil
	}
}

func (m *UI) copyChatHighlight() tea.Cmd {
	text := m.chat.HighlightContent()
	return common.CopyToClipboardWithCallback(
		text,
		"Selected text copied to clipboard",
		func() tea.Msg {
			m.chat.ClearMouse()
			return nil
		},
	)
}

// runExtensionCommand expands a Lua extension's command into a prompt
// and sends it. The handler runs in the extension's VM, so it is done in
// a command rather than inline in Update.
func (m *UI) runExtensionCommand(commandID string, args map[string]string) tea.Cmd {
	return func() tea.Msg {
		prompt, err := m.com.Workspace.RunExtensionCommand(context.Background(), commandID, args)
		if err != nil {
			slog.Error("Failed to run extension command", "command", commandID, "error", err)
			return util.ReportError(err)()
		}
		if strings.TrimSpace(prompt) == "" {
			return nil
		}
		return extensionCommandExpandedMsg{Prompt: prompt}
	}
}

// extensionCommandExpandedMsg carries the prompt an extension command
// produced back to the UI loop, which sends it as the user's message.
type extensionCommandExpandedMsg struct {
	Prompt string
}
