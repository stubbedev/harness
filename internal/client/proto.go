package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/crash"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
)

// ListWorkspaces retrieves all workspaces from the server.
func (c *Client) ListWorkspaces(ctx context.Context) ([]proto.Workspace, error) {
	return call[[]proto.Workspace](ctx, c, "list workspaces", get(apiPath("workspaces")))
}

// CreateWorkspace creates a new workspace on the server.
func (c *Client) CreateWorkspace(ctx context.Context, ws proto.Workspace) (*proto.Workspace, error) {
	ws.ClientID = c.clientID
	created, err := call[proto.Workspace](ctx, c, "create workspace", post(apiPath("workspaces"), ws))
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// GetWorkspace retrieves a workspace from the server.
func (c *Client) GetWorkspace(ctx context.Context, id string) (*proto.Workspace, error) {
	ws, err := call[proto.Workspace](ctx, c, "get workspace", get(wsPath(id)))
	if err != nil {
		return nil, err
	}
	return &ws, nil
}

// DeleteWorkspace deletes a workspace on the server.
func (c *Client) DeleteWorkspace(ctx context.Context, id string) error {
	return c.do(ctx, "delete workspace", request{
		method: http.MethodDelete,
		path:   wsPath(id),
		query:  url.Values{"client_id": []string{c.clientID}},
	})
}

// SetCurrentSession reports the client's current-session selection
// for the named workspace. An empty sessionID clears the entry. The
// request carries the process-scoped client ID minted in [NewClient]
// as a query parameter so the server can route the update to the
// correct [clientState] entry.
func (c *Client) SetCurrentSession(ctx context.Context, workspaceID, sessionID string) error {
	r := post(wsPath(workspaceID, "current-session"), proto.CurrentSession{SessionID: sessionID})
	r.query = url.Values{"client_id": []string{c.clientID}}
	return c.do(ctx, "set current session", r)
}

// SubscribeEvents subscribes to server-sent events for a workspace.
func (c *Client) SubscribeEvents(ctx context.Context, id string) (<-chan any, error) {
	events := make(chan any, 100)
	q := url.Values{"client_id": []string{c.clientID}}
	//nolint:bodyclose
	rsp, err := c.get(ctx, wsPath(id, "events"), q, http.Header{
		"Accept":        []string{"text/event-stream"},
		"Cache-Control": []string{"no-cache"},
		"Connection":    []string{"keep-alive"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to events: %w", err)
	}

	if err := checkStatus(rsp); err != nil {
		rsp.Body.Close()
		return nil, fmt.Errorf("failed to subscribe to events: %w", err)
	}

	go func() {
		defer crash.Recover("client.eventStream", nil)
		defer rsp.Body.Close()
		defer close(events)

		scr := bufio.NewReader(rsp.Body)
		for {
			line, err := scr.ReadBytes('\n')
			if err != nil {
				// Any read error is terminal: the body keeps returning
				// it, so retrying the read would spin forever. Closing
				// the channel hands recovery to the caller's reconnect
				// loop.
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					slog.Error("Reading from events stream", "error", err)
				}
				return
			}
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}

			data, ok := bytes.CutPrefix(line, []byte("data:"))
			if !ok {
				slog.Warn("Invalid event format", "line", string(line))
				continue
			}

			data = bytes.TrimSpace(data)

			var p pubsub.Payload
			if err := json.Unmarshal(data, &p); err != nil {
				slog.Error("Unmarshaling event envelope", "error", err)
				continue
			}

			decode, ok := eventDecoders[p.Type]
			if !ok {
				slog.Warn("Unknown event type", "type", p.Type)
				continue
			}
			e, err := decode(p.Payload)
			if err != nil {
				// A zero-value event would reach the UI as, say, an
				// empty message; dropping it is the lesser harm.
				slog.Error("Unmarshaling event payload", "type", p.Type, "error", err)
				continue
			}
			if !sendEvent(ctx, events, e) {
				return
			}
		}
	}()

	return events, nil
}

// eventDecoders maps each SSE payload type to the decoder for its
// typed pubsub event.
var eventDecoders = map[pubsub.PayloadType]func(json.RawMessage) (any, error){
	pubsub.PayloadTypeLSPEvent:             decodeEvent[proto.LSPEvent],
	pubsub.PayloadTypeMCPEvent:             decodeEvent[proto.MCPEvent],
	pubsub.PayloadTypeQuestionRequest:      decodeEvent[proto.QuestionRequest],
	pubsub.PayloadTypeQuestionNotification: decodeEvent[proto.QuestionNotification],
	pubsub.PayloadTypeMessage:              decodeEvent[proto.Message],
	pubsub.PayloadTypeSession:              decodeEvent[proto.Session],
	pubsub.PayloadTypeFile:                 decodeEvent[proto.File],
	pubsub.PayloadTypeAgentEvent:           decodeEvent[proto.AgentEvent],
	pubsub.PayloadTypeConfigChanged:        decodeEvent[proto.ConfigChanged],
	pubsub.PayloadTypeSkillsEvent:          decodeEvent[proto.SkillsEvent],
	pubsub.PayloadTypeRunComplete:          decodeEvent[proto.RunComplete],
	pubsub.PayloadTypeUpdateAvailable:      decodeEvent[proto.UpdateAvailable],
}

func decodeEvent[T any](raw json.RawMessage) (any, error) {
	var e pubsub.Event[T]
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	return e, nil
}

func sendEvent(ctx context.Context, evc chan any, ev any) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case evc <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// GetLSPDiagnostics retrieves LSP diagnostics for a specific LSP client.
func (c *Client) GetLSPDiagnostics(ctx context.Context, id string, lspName string) (map[protocol.DocumentURI][]protocol.Diagnostic, error) {
	return call[map[protocol.DocumentURI][]protocol.Diagnostic](ctx, c, "get LSP diagnostics", get(wsPath(id, "lsps", lspName, "diagnostics")))
}

// GetLSPs retrieves the LSP client states for a workspace.
func (c *Client) GetLSPs(ctx context.Context, id string) (map[string]proto.LSPClientInfo, error) {
	return call[map[string]proto.LSPClientInfo](ctx, c, "get LSPs", get(wsPath(id, "lsps")))
}

// MCPGetStates retrieves the MCP client states for a workspace.
func (c *Client) MCPGetStates(ctx context.Context, id string) (map[string]proto.MCPClientInfo, error) {
	return call[map[string]proto.MCPClientInfo](ctx, c, "get MCP states", get(wsPath(id, "mcp", "states")))
}

// MCPPendingAuth retrieves the MCP servers awaiting OAuth authentication
// for a workspace.
func (c *Client) MCPPendingAuth(ctx context.Context, id string) ([]proto.MCPPendingAuthServer, error) {
	return call[[]proto.MCPPendingAuthServer](ctx, c, "get MCP pending auth", get(wsPath(id, "mcp", "pending-auth")))
}

// MCPAuthURL retrieves the current OAuth authorization URL for a named MCP
// server, if a flow is in progress.
func (c *Client) MCPAuthURL(ctx context.Context, id, name string) (string, error) {
	resp, err := call[proto.MCPAuthResponse](ctx, c, "get MCP auth URL",
		get(wsPath(id, "mcp", "auth-url"), url.Values{"name": []string{name}}))
	return resp.AuthURL, err
}

// MCPAuthenticate runs the OAuth flow for a named MCP server. The server's
// local browser is suppressed; the caller is responsible for surfacing the
// authorization URL (via polling [Client.MCPPendingAuth] / state events)
// and opening it on the user's machine. The call blocks until the flow
// completes, fails, or ctx is cancelled.
func (c *Client) MCPAuthenticate(ctx context.Context, id, name string) error {
	return c.do(ctx, "authenticate MCP", post(wsPath(id, "mcp", "auth"), proto.MCPNameRequest{Name: name}))
}

// MCPReconnect restarts a named MCP server, clearing a session-scoped
// disable.
func (c *Client) MCPReconnect(ctx context.Context, id, name string) error {
	return c.do(ctx, "reconnect MCP", post(wsPath(id, "mcp", "reconnect"), proto.MCPNameRequest{Name: name}))
}

// MCPDisableForSession disables a named MCP server for the rest of the
// server process without touching its configuration.
func (c *Client) MCPDisableForSession(ctx context.Context, id, name string) error {
	return c.do(ctx, "disable MCP", post(wsPath(id, "mcp", "disable"), proto.MCPNameRequest{Name: name}))
}

// MCPRefreshPrompts refreshes prompts for a named MCP client.
func (c *Client) MCPRefreshPrompts(ctx context.Context, id, name string) error {
	return c.do(ctx, "refresh MCP prompts", post(wsPath(id, "mcp", "refresh-prompts"), proto.MCPNameRequest{Name: name}))
}

// MCPRefreshResources refreshes resources for a named MCP client.
func (c *Client) MCPRefreshResources(ctx context.Context, id, name string) error {
	return c.do(ctx, "refresh MCP resources", post(wsPath(id, "mcp", "refresh-resources"), proto.MCPNameRequest{Name: name}))
}

// GetAgentSessionQueuedPrompts retrieves the number of queued prompts for a
// session.
func (c *Client) GetAgentSessionQueuedPrompts(ctx context.Context, id string, sessionID string) (int, error) {
	return call[int](ctx, c, "get queued prompts", get(wsPath(id, "agent", "sessions", sessionID, "prompts", "queued")))
}

// ClearAgentSessionQueuedPrompts clears the queued prompts for a session.
func (c *Client) ClearAgentSessionQueuedPrompts(ctx context.Context, id string, sessionID string) error {
	return c.do(ctx, "clear queued prompts", post(wsPath(id, "agent", "sessions", sessionID, "prompts", "clear"), nil))
}

// GetAgentInfo retrieves the agent status for a workspace.
func (c *Client) GetAgentInfo(ctx context.Context, id string) (*proto.AgentInfo, error) {
	info, err := call[proto.AgentInfo](ctx, c, "get agent status", get(wsPath(id, "agent")))
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// UpdateAgent triggers an agent model update on the server.
func (c *Client) UpdateAgent(ctx context.Context, id string) error {
	return c.do(ctx, "update agent", post(wsPath(id, "agent", "update"), nil))
}

// SendMessage sends a message to the agent for a workspace.
//
// When runID is non-empty it is echoed back on the resulting
// proto.RunComplete event, giving the caller a unique correlator
// for completion detection. Pass "" when the caller does not need
// to distinguish its own turn's terminal event from any concurrent
// turn on the same session (e.g. interactive TUI usage).
func (c *Client) SendMessage(ctx context.Context, id string, sessionID, runID, prompt string, attachments ...message.Attachment) error {
	r := post(wsPath(id, "agent"), proto.AgentMessage{
		SessionID:   sessionID,
		RunID:       runID,
		Prompt:      prompt,
		Attachments: proto.AttachmentsFromMessage(attachments),
	})
	r.ok = []int{http.StatusOK, http.StatusAccepted}
	return c.do(ctx, "send message to agent", r)
}

// RunShellCommand runs a shell command in the workspace without triggering
// the agent. isFirstMessage asks the server to title the session from it.
func (c *Client) RunShellCommand(ctx context.Context, id, sessionID, command string, termWidth int, isFirstMessage bool) (proto.ShellCommandResponse, error) {
	return call[proto.ShellCommandResponse](ctx, c, "run shell command", post(wsPath(id, "agent", "sessions", sessionID, "shell"), proto.ShellCommandRequest{
		Command:        command,
		TermWidth:      termWidth,
		IsFirstMessage: isFirstMessage,
	}))
}

// GetAgentSessionInfo retrieves the agent session info for a workspace.
func (c *Client) GetAgentSessionInfo(ctx context.Context, id string, sessionID string) (*proto.AgentSession, error) {
	info, err := call[proto.AgentSession](ctx, c, "get agent session", get(wsPath(id, "agent", "sessions", sessionID)))
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// AgentSummarizeSession requests a session summarization. instructions,
// when non-empty, steer the summary's focus (/compact input).
func (c *Client) AgentSummarizeSession(ctx context.Context, id string, sessionID string, instructions string) error {
	var body any
	if instructions != "" {
		body = proto.SummarizeRequest{Instructions: instructions}
	}
	return c.do(ctx, "summarize session", post(wsPath(id, "agent", "sessions", sessionID, "summarize"), body))
}

// InitiateAgentProcessing triggers agent initialization on the server.
func (c *Client) InitiateAgentProcessing(ctx context.Context, id string, interactive bool) error {
	return c.do(ctx, "initialize agent", post(wsPath(id, "agent", "init"), proto.AgentInitRequest{Interactive: interactive}))
}

// ListMessages retrieves all messages for a session as proto types.
func (c *Client) ListMessages(ctx context.Context, id string, sessionID string) ([]proto.Message, error) {
	return call[[]proto.Message](ctx, c, "get messages", get(wsPath(id, "sessions", sessionID, "messages")))
}

// GetSession retrieves a specific session as a proto type.
func (c *Client) GetSession(ctx context.Context, id string, sessionID string) (*proto.Session, error) {
	sess, err := call[proto.Session](ctx, c, "get session", get(wsPath(id, "sessions", sessionID)))
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// ListSessionHistoryFiles retrieves history files for a session as proto types.
func (c *Client) ListSessionHistoryFiles(ctx context.Context, id string, sessionID string) ([]proto.File, error) {
	return call[[]proto.File](ctx, c, "get session history", get(wsPath(id, "sessions", sessionID, "history")))
}

// CreateSession creates a new session in a workspace as a proto type.
func (c *Client) CreateSession(ctx context.Context, id string, title string) (*proto.Session, error) {
	sess, err := call[proto.Session](ctx, c, "create session", post(wsPath(id, "sessions"), proto.Session{Title: title}))
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// ListSessions lists all sessions in a workspace as proto types.
func (c *Client) ListSessions(ctx context.Context, id string) ([]proto.Session, error) {
	return call[[]proto.Session](ctx, c, "get sessions", get(wsPath(id, "sessions")))
}

// AnswerQuestionBatch submits answers for a batch question on a
// workspace. Returns true if this call resolved the pending
// request, false if already resolved by another caller.
func (c *Client) AnswerQuestionBatch(ctx context.Context, id string, req proto.QuestionAnswer) (bool, error) {
	resp, err := call[proto.QuestionAnswerResponse](ctx, c, "answer question batch", post(wsPath(id, "questions", "answer"), req))
	return resp.Resolved, err
}

// CancelQuestionBatch cancels the pending question batch on a
// workspace. Returns true if a question was cancelled, false if
// none was pending.
func (c *Client) CancelQuestionBatch(ctx context.Context, id string) (bool, error) {
	resp, err := call[proto.QuestionAnswerResponse](ctx, c, "cancel question batch", post(wsPath(id, "questions", "cancel"), nil))
	return resp.Resolved, err
}

// GetConfig retrieves the workspace-specific configuration.
func (c *Client) GetConfig(ctx context.Context, id string) (*config.Config, error) {
	cfg, err := call[config.Config](ctx, c, "get config", get(wsPath(id, "config")))
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// RenameSession changes only the title of a session in a workspace.
func (c *Client) RenameSession(ctx context.Context, id, sessionID, title string) error {
	return c.do(ctx, "rename session", request{
		method: http.MethodPut,
		path:   wsPath(id, "sessions", sessionID),
		body:   proto.SessionRenameRequest{Title: title},
	})
}

// DeleteSession deletes a session from a workspace.
func (c *Client) DeleteSession(ctx context.Context, id string, sessionID string) error {
	return c.do(ctx, "delete session", request{method: http.MethodDelete, path: wsPath(id, "sessions", sessionID)})
}

// ListUserMessages retrieves user-role messages for a session as proto types.
func (c *Client) ListUserMessages(ctx context.Context, id string, sessionID string) ([]proto.Message, error) {
	return call[[]proto.Message](ctx, c, "get user messages", get(wsPath(id, "sessions", sessionID, "messages", "user")))
}

// ListAllUserMessages retrieves all user-role messages across sessions as proto types.
func (c *Client) ListAllUserMessages(ctx context.Context, id string) ([]proto.Message, error) {
	return call[[]proto.Message](ctx, c, "get all user messages", get(wsPath(id, "messages", "user")))
}

// CancelAgentSession cancels an ongoing agent operation for a session.
func (c *Client) CancelAgentSession(ctx context.Context, id string, sessionID string) error {
	return c.do(ctx, "cancel agent session", post(wsPath(id, "agent", "sessions", sessionID, "cancel"), nil))
}

// CancelAgentSessionTurn interrupts the session's active run only;
// queued prompts survive and run once the interrupted turn unwinds.
func (c *Client) CancelAgentSessionTurn(ctx context.Context, id string, sessionID string) error {
	return c.do(ctx, "cancel agent session turn", post(wsPath(id, "agent", "sessions", sessionID, "cancel-turn"), nil))
}

// GetAgentSessionQueuedPromptsList retrieves the list of queued prompt
// strings for a session.
func (c *Client) GetAgentSessionQueuedPromptsList(ctx context.Context, id string, sessionID string) ([]string, error) {
	return call[[]string](ctx, c, "get queued prompts list", get(wsPath(id, "agent", "sessions", sessionID, "prompts", "list")))
}

// GetDefaultSmallModel retrieves the default small model for a provider.
func (c *Client) GetDefaultSmallModel(ctx context.Context, id string, providerID string) (*config.SelectedModel, error) {
	model, err := call[config.SelectedModel](ctx, c, "get default small model",
		get(wsPath(id, "agent", "default-small-model"), url.Values{"provider_id": []string{providerID}}))
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// FileTrackerRecordRead records a file read for a session.
func (c *Client) FileTrackerRecordRead(ctx context.Context, id string, sessionID, path string) error {
	return c.do(ctx, "record file read", post(wsPath(id, "filetracker", "read"), proto.FileTrackerReadRequest{SessionID: sessionID, Path: path}))
}

// FileTrackerLastReadTime returns the last read time for a file in a
// session.
func (c *Client) FileTrackerLastReadTime(ctx context.Context, id string, sessionID, path string) (time.Time, error) {
	return call[time.Time](ctx, c, "get last read time", get(wsPath(id, "filetracker", "lastread"), url.Values{
		"session_id": []string{sessionID},
		"path":       []string{path},
	}))
}

// FileTrackerListReadFiles returns the list of read files for a session.
func (c *Client) FileTrackerListReadFiles(ctx context.Context, id string, sessionID string) ([]string, error) {
	return call[[]string](ctx, c, "get read files", get(wsPath(id, "sessions", sessionID, "filetracker", "files")))
}

// LSPStart starts an LSP server for a path.
func (c *Client) LSPStart(ctx context.Context, id string, path string) error {
	return c.do(ctx, "start LSP", post(wsPath(id, "lsps", "start"), proto.LSPStartRequest{Path: path}))
}

// LSPStopAll stops all LSP servers for a workspace.
func (c *Client) LSPStopAll(ctx context.Context, id string) error {
	return c.do(ctx, "stop LSPs", post(wsPath(id, "lsps", "stop"), nil))
}

// LSPRestartSingle restarts a named running LSP server.
func (c *Client) LSPRestartSingle(ctx context.Context, id, name string) error {
	return c.do(ctx, "restart LSP", post(wsPath(id, "lsps", "restart"), proto.LSPNameRequest{Name: name}))
}

// LSPSetSessionDisabled turns a named LSP server off (or back on) for
// the rest of the server process without touching its configuration.
func (c *Client) LSPSetSessionDisabled(ctx context.Context, id, name string, disabled bool) error {
	action := "enable"
	if disabled {
		action = "disable"
	}
	return c.do(ctx, "set LSP session state", post(wsPath(id, "lsps", action), proto.LSPNameRequest{Name: name}))
}

// ListCheckpoints retrieves a session's rewind checkpoints.
func (c *Client) ListCheckpoints(ctx context.Context, id string, sessionID string) ([]proto.Checkpoint, error) {
	return call[[]proto.Checkpoint](ctx, c, "get checkpoints", get(wsPath(id, "sessions", sessionID, "checkpoints")))
}

// RewindSession rewinds a session to an earlier turn.
func (c *Client) RewindSession(ctx context.Context, id, sessionID, messageID, mode string) error {
	return c.do(ctx, "rewind session", post(wsPath(id, "sessions", sessionID, "rewind"), proto.RewindRequest{MessageID: messageID, Mode: mode}))
}
