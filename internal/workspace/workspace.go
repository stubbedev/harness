// Package workspace defines the Workspace interface used by all
// frontends (TUI, CLI) to interact with a running workspace. Two
// implementations exist: one wrapping a local app.App instance and one
// wrapping the HTTP client SDK.
package workspace

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/skills"
)

// Reasons the coder agent may be unavailable, returned by
// Workspace.AgentReadyErr so callers can tell a genuinely
// uninitialized agent apart from a lost server connection.
var (
	// ErrAgentNotInitialized means the workspace exists but its coder
	// agent has not been configured/initialized (e.g. no model set).
	ErrAgentNotInitialized = errors.New("coder agent is not initialized")
	// ErrServerUnreachable means the client could not reach the server
	// to determine the agent's status (server down, or the workspace was
	// torn down out from under the client).
	ErrServerUnreachable = errors.New("lost connection to the harness server")
	// ErrWorkspaceGone means the server is reachable but no longer knows
	// this client's workspace: it was torn down, or the server was
	// replaced underneath the client. The subscription loop re-registers
	// the workspace in the background when it sees this.
	ErrWorkspaceGone = errors.New("the server reset this workspace; reconnecting")
	// ErrStreamClosed means an established event stream ended.
	// Resubscribing usually succeeds immediately, but events published in
	// the meantime are lost for good, so the client treats it as a
	// degraded link that requires a resync.
	ErrStreamClosed = errors.New("the event stream closed; reconnecting")
	// ErrSessionBusy means the request is refused because an agent run is
	// in flight for the session, such as a rewind that would race the
	// tools still writing to the tree.
	ErrSessionBusy = errors.New("the agent is running in this session")
	// ErrInvalidArgument means a request value was malformed.
	ErrInvalidArgument = errors.New("invalid argument")
)

// ConnectionState describes the health of the client-server link as
// reported by the [ClientWorkspace] subscription loop.
type ConnectionState int

const (
	// ConnectionDegraded means the event stream is down (or the workspace
	// was lost server-side) and the client is retrying or re-registering
	// in the background.
	ConnectionDegraded ConnectionState = iota
	// ConnectionRecovered means the event stream was re-established,
	// possibly against a re-created workspace.
	ConnectionRecovered
)

// ConnectionEvent is delivered to the TUI as a tea.Msg on degraded and
// recovered transitions of the client-server link. Local (in-process)
// workspaces never emit it.
type ConnectionEvent struct {
	State ConnectionState
	// Err is the most recent failure, set when State is
	// ConnectionDegraded.
	Err error
	// Stuck marks a degraded connection that has resisted repeated
	// recovery attempts. The loop keeps retrying regardless; the UI
	// should escalate from a transient notice to a persistent error.
	Stuck bool
}

// LSPClientInfo holds information about an LSP client's state. This is
// the frontend-facing type; implementations translate from the
// underlying app or proto representation.
type LSPClientInfo struct {
	Name            string
	State           lsp.ServerState
	Error           error
	DiagnosticCount int
	ConnectedAt     time.Time
	// SessionDisabled marks a server turned off at runtime for the rest
	// of the process, so the UI can tell it from a merely stopped one.
	SessionDisabled bool
}

// StatusText returns the human-readable status of the server: the one
// line a server listing shows next to the server name.
func (i LSPClientInfo) StatusText() string {
	if i.SessionDisabled {
		return "disabled for this session"
	}
	switch i.State {
	case lsp.StateUnstarted:
		return "not started yet"
	case lsp.StateStopped:
		return "stopped"
	case lsp.StateStarting:
		return "starting…"
	case lsp.StateReady:
		return "ready"
	case lsp.StateError:
		if i.Error != nil {
			return "error: " + i.Error.Error()
		}
		return "error"
	case lsp.StateDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// LSPEventType represents the type of LSP event.
type LSPEventType string

const (
	LSPEventStateChanged       LSPEventType = "state_changed"
	LSPEventDiagnosticsChanged LSPEventType = "diagnostics_changed"
)

// LSPEvent represents an LSP event forwarded to the TUI.
type LSPEvent struct {
	Type            LSPEventType
	Name            string
	State           lsp.ServerState
	Error           error
	DiagnosticCount int
}

// AgentModel holds the model information exposed to the UI.
type AgentModel struct {
	CatalogCfg catalog.Model
	ModelCfg   config.SelectedModel
}

// Workspace is the main abstraction consumed by the TUI and CLI. It
// groups every operation a frontend needs to perform against a running
// workspace, regardless of whether the workspace is in-process or
// remote.
type Workspace interface {
	// Sessions
	CreateSession(ctx context.Context, title string) (session.Session, error)
	GetSession(ctx context.Context, sessionID string) (session.Session, error)
	ListSessions(ctx context.Context) ([]session.Session, error)
	// RenameSession changes only the session's title; usage, summary
	// and compaction fields are never rewritten from a client copy.
	RenameSession(ctx context.Context, sessionID, title string) error
	DeleteSession(ctx context.Context, sessionID string) error
	CreateAgentToolSessionID(messageID, toolCallID string) string
	ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool)
	// SetCurrentSession reports the session this client is currently
	// viewing. Empty sessionID clears the entry (e.g. landing screen).
	// In single-client local mode this is a no-op. In client/server
	// mode it informs the server's per-client presence map so other
	// observers can compute attached-client counts per session.
	SetCurrentSession(ctx context.Context, sessionID string) error

	// Messages
	ListMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	ListUserMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	ListAllUserMessages(ctx context.Context) ([]message.Message, error)

	// Checkpoints
	// ListCheckpoints returns the per-turn working-tree snapshots
	// recorded for a session, oldest first.
	ListCheckpoints(ctx context.Context, sessionID string) ([]checkpoints.Checkpoint, error)
	// Rewind restores a session to the state just before the given
	// user message was sent: the transcript, the files on disk, or
	// both. It fails while the session is busy.
	Rewind(ctx context.Context, sessionID, messageID string, mode checkpoints.Mode) error

	// Agent
	AgentRun(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) error
	AgentRunShellCommand(ctx context.Context, sessionID, command string, termWidth int, onProgress func(string), isFirstMessage bool) (proto.ShellCommandResponse, error)
	AgentCancel(sessionID string)
	// AgentCancelTurn interrupts the session's active run only: queued
	// prompts survive and run once the interrupted turn unwinds. It is
	// the interrupt-and-steer escape path; AgentCancel is the
	// drop-everything variant.
	AgentCancelTurn(sessionID string)
	AgentIsBusy() bool
	AgentIsSessionBusy(sessionID string) bool
	AgentModel() AgentModel
	AgentIsReady() bool
	// AgentReadyErr reports nil when the coder agent is ready to accept
	// work, or a descriptive error otherwise: ErrAgentNotInitialized
	// when the agent simply isn't set up, or ErrServerUnreachable
	// (wrapped) when the client could not reach the server to find out.
	// It lets the UI show an actionable message instead of collapsing
	// both cases into "agent offline".
	AgentReadyErr() error
	AgentQueuedPrompts(sessionID string) int
	AgentQueuedPromptsList(sessionID string) []string
	AgentClearQueue(sessionID string)
	AgentSummarize(ctx context.Context, sessionID, instructions string) error
	UpdateAgentModel(ctx context.Context) error
	InitCoderAgent(ctx context.Context) error
	InitCoderAgentNonInteractive(ctx context.Context) error
	GetDefaultSmallModel(providerID string) config.SelectedModel

	// Questions
	//
	// QuestionAnswer resolves the pending question with responses.
	QuestionAnswer(responses []question.Answer) bool

	// QuestionCancel cancels the pending question.
	QuestionCancel() bool

	// FileTracker
	FileTrackerRecordRead(ctx context.Context, sessionID, path string)
	FileTrackerLastReadTime(ctx context.Context, sessionID, path string) time.Time
	FileTrackerListReadFiles(ctx context.Context, sessionID string) ([]string, error)

	// History
	ListSessionHistory(ctx context.Context, sessionID string) ([]history.File, error)

	// LSP
	LSPStart(ctx context.Context, path string)
	LSPStopAll(ctx context.Context)
	LSPGetStates() map[string]LSPClientInfo
	LSPGetDiagnosticCounts(name string) lsp.DiagnosticCounts
	// LSPFileDiagnostics aggregates every server's current diagnostics into
	// per-file severity counts. It backs the transcript's live diagnostics
	// overlay, which needs to know that a file is clean now, not just that
	// some server somewhere still counts problems.
	LSPFileDiagnostics() map[string]lsp.DiagnosticCounts
	LSPRestartSingle(ctx context.Context, name string) error
	LSPSetSessionDisabled(ctx context.Context, name string, disabled bool) error

	// Config (read-only data)
	Config() *config.Config
	WorkingDir() string
	Resolver() config.VariableResolver

	// Config mutations (proxied to server in client mode)
	UpdatePreferredModel(scope config.Scope, modelType config.SelectedModelType, model config.SelectedModel) error
	SetCompactMode(scope config.Scope, enabled bool) error
	SetProviderAPIKey(scope config.Scope, providerID string, apiKey any) error
	SetConfigField(scope config.Scope, key string, value any) error
	RemoveConfigField(scope config.Scope, key string) error
	ImportCopilot() (*oauth.Token, bool)
	RefreshOAuthToken(ctx context.Context, scope config.Scope, providerID string) error

	// Project lifecycle
	InitializePrompt() (string, error)
	ListSkills(ctx context.Context) ([]skills.CatalogEntry, error)
	ReadSkill(ctx context.Context, skillID string) ([]byte, skills.SkillReadResult, error)
	ListExtensionCommands(ctx context.Context) ([]extensions.Command, error)
	RunExtensionCommand(ctx context.Context, commandID string, args map[string]string) (string, error)
	ActiveSubagents() []SubagentInfo
	RunningSubagents(parentSessionID string) []RunningSubagentInfo
	CancelSubagent(childSessionID string)
	AllSubagents() []SubagentDefInfo
	DeleteUserSubagent(name string) error
	SetSubagentDisabled(name string, disabled bool) error

	// MCP operations (server-side in client mode)
	MCPGetStates() map[string]mcptools.ClientInfo
	MCPRefreshPrompts(ctx context.Context, name string)
	MCPRefreshResources(ctx context.Context, name string)
	RefreshMCPTools(ctx context.Context, name string)
	ReadMCPResource(ctx context.Context, name, uri string) ([]MCPResourceContents, error)
	ListMCPPrompts(ctx context.Context) ([]commands.MCPPrompt, error)
	GetMCPPrompt(clientID, promptID string, args map[string]string) (string, error)
	MCPAuthenticate(ctx context.Context, name string) error
	MCPPendingAuth() []mcptools.PendingAuthServer
	MCPAuthURL(name string) string
	MCPReconnect(ctx context.Context, name string) error
	MCPDisableForSession(ctx context.Context, name string) error

	// Events
	Subscribe(program *tea.Program)
	Shutdown()
}

// SubagentInfo holds the minimal frontend-facing data for an active subagent.
type SubagentInfo struct {
	Name        string
	Description string
}

// RunningSubagentInfo holds frontend-facing data for a currently running
// subagent instance, enriched with session token counts.
type RunningSubagentInfo struct {
	ChildSessionID   string
	ParentSessionID  string
	Name             string
	Color            string
	Model            string
	Status           string
	StartedAt        time.Time
	PromptTokens     int64
	CompletionTokens int64
}

// SubagentDefInfo holds frontend-facing data for a discovered subagent
// definition, including its scope relative to the workspace.
type SubagentDefInfo struct {
	Name        string
	Description string
	Color       string
	FilePath    string
	Scope       string // "user", "project", or "builtin"
	Disabled    bool
	// Deletable reports whether the definition lives in a user-owned global
	// subagents directory and may be removed via DeleteUserSubagent. Scope is
	// display-oriented and does not imply this; broken (Error) entries are
	// never deletable.
	Deletable bool
	// Error carries the discovery diagnostic when the definition file failed
	// to parse or validate. Such entries are informational only: they cannot
	// be dispatched, toggled, or deleted.
	Error string
}

// MCPResourceContents holds the contents of an MCP resource.
type MCPResourceContents = proto.MCPResourceContents
