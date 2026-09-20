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
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
)

// ListWorkspaces retrieves all workspaces from the server.
func (c *Client) ListWorkspaces(ctx context.Context) ([]proto.Workspace, error) {
	rsp, err := c.get(ctx, "/workspaces", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list workspaces: %w", err)
	}
	var workspaces []proto.Workspace
	if err := decodeJSON(rsp, &workspaces, "failed to list workspaces", "workspaces"); err != nil {
		return nil, err
	}
	return workspaces, nil
}

// CreateWorkspace creates a new workspace on the server.
func (c *Client) CreateWorkspace(ctx context.Context, ws proto.Workspace) (*proto.Workspace, error) {
	ws.ClientID = c.clientID
	rsp, err := c.post(ctx, "/workspaces", nil, jsonBody(ws), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}
	defer rsp.Body.Close()
	if err := checkStatus(rsp); err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}
	var created proto.Workspace
	if err := json.NewDecoder(rsp.Body).Decode(&created); err != nil {
		return nil, fmt.Errorf("failed to decode workspace: %w", err)
	}
	return &created, nil
}

// GetWorkspace retrieves a workspace from the server.
func (c *Client) GetWorkspace(ctx context.Context, id string) (*proto.Workspace, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace: %w", err)
	}
	defer rsp.Body.Close()
	if err := checkStatus(rsp); err != nil {
		return nil, fmt.Errorf("failed to get workspace: %w", err)
	}
	var ws proto.Workspace
	if err := json.NewDecoder(rsp.Body).Decode(&ws); err != nil {
		return nil, fmt.Errorf("failed to decode workspace: %w", err)
	}
	return &ws, nil
}

// DeleteWorkspace deletes a workspace on the server.
func (c *Client) DeleteWorkspace(ctx context.Context, id string) error {
	q := url.Values{"client_id": []string{c.clientID}}
	rsp, err := c.delete(ctx, fmt.Sprintf("/workspaces/%s", id), q, nil)
	if err != nil {
		return fmt.Errorf("failed to delete workspace: %w", err)
	}
	if err := okOrError(rsp, "failed to delete workspace"); err != nil {
		return err
	}
	return nil
}

// SetCurrentSession reports the client's current-session selection
// for the named workspace. An empty sessionID clears the entry. The
// request carries the process-scoped client ID minted in [NewClient]
// as a query parameter so the server can route the update to the
// correct [clientState] entry.
func (c *Client) SetCurrentSession(ctx context.Context, workspaceID, sessionID string) error {
	q := url.Values{"client_id": []string{c.clientID}}
	rsp, err := c.post(
		ctx,
		fmt.Sprintf("/workspaces/%s/current-session", workspaceID),
		q,
		jsonBody(proto.CurrentSession{SessionID: sessionID}),
		http.Header{"Content-Type": []string{"application/json"}},
	)
	if err != nil {
		return fmt.Errorf("failed to set current session: %w", err)
	}
	defer rsp.Body.Close()
	if err := checkStatus(rsp); err != nil {
		return fmt.Errorf("failed to set current session: %w", err)
	}
	return nil
}

// SubscribeEvents subscribes to server-sent events for a workspace.
func (c *Client) SubscribeEvents(ctx context.Context, id string) (<-chan any, error) {
	events := make(chan any, 100)
	q := url.Values{"client_id": []string{c.clientID}}
	//nolint:bodyclose
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/events", id), q, http.Header{
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
		defer rsp.Body.Close()
		defer close(events)

		scr := bufio.NewReader(rsp.Body)
		for {
			line, err := scr.ReadBytes('\n')
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Error("Reading from events stream", "error", err)
				select {
				case <-time.After(time.Second * 2):
				case <-ctx.Done():
					return
				}
				continue
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

			switch p.Type {
			case pubsub.PayloadTypeLSPEvent:
				var e pubsub.Event[proto.LSPEvent]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeMCPEvent:
				var e pubsub.Event[proto.MCPEvent]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeQuestionRequest:
				var e pubsub.Event[proto.QuestionRequest]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeQuestionNotification:
				var e pubsub.Event[proto.QuestionNotification]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeMessage:
				var e pubsub.Event[proto.Message]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeSession:
				var e pubsub.Event[proto.Session]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeFile:
				var e pubsub.Event[proto.File]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeAgentEvent:
				var e pubsub.Event[proto.AgentEvent]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeConfigChanged:
				var e pubsub.Event[proto.ConfigChanged]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeSkillsEvent:
				var e pubsub.Event[proto.SkillsEvent]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeRunComplete:
				var e pubsub.Event[proto.RunComplete]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			case pubsub.PayloadTypeUpdateAvailable:
				var e pubsub.Event[proto.UpdateAvailable]
				_ = json.Unmarshal(p.Payload, &e)
				if !sendEvent(ctx, events, e) {
					return
				}
			default:
				slog.Warn("Unknown event type", "type", p.Type)
				continue
			}
		}
	}()

	return events, nil
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
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/lsps/%s/diagnostics", id, lspName), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get LSP diagnostics: %w", err)
	}
	var diagnostics map[protocol.DocumentURI][]protocol.Diagnostic
	if err := decodeJSON(rsp, &diagnostics, "failed to get LSP diagnostics", "LSP diagnostics"); err != nil {
		return nil, err
	}
	return diagnostics, nil
}

// GetLSPs retrieves the LSP client states for a workspace.
func (c *Client) GetLSPs(ctx context.Context, id string) (map[string]proto.LSPClientInfo, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/lsps", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get LSPs: %w", err)
	}
	var lsps map[string]proto.LSPClientInfo
	if err := decodeJSON(rsp, &lsps, "failed to get LSPs", "LSPs"); err != nil {
		return nil, err
	}
	return lsps, nil
}

// MCPGetStates retrieves the MCP client states for a workspace.
func (c *Client) MCPGetStates(ctx context.Context, id string) (map[string]proto.MCPClientInfo, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/mcp/states", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCP states: %w", err)
	}
	var states map[string]proto.MCPClientInfo
	if err := decodeJSON(rsp, &states, "failed to get MCP states", "MCP states"); err != nil {
		return nil, err
	}
	return states, nil
}

// MCPPendingAuth retrieves the MCP servers awaiting OAuth authentication
// for a workspace.
func (c *Client) MCPPendingAuth(ctx context.Context, id string) ([]proto.MCPPendingAuthServer, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/mcp/pending-auth", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCP pending auth: %w", err)
	}
	var pending []proto.MCPPendingAuthServer
	if err := decodeJSON(rsp, &pending, "failed to get MCP pending auth", "MCP pending auth"); err != nil {
		return nil, err
	}
	return pending, nil
}

// MCPAuthURL retrieves the current OAuth authorization URL for a named MCP
// server, if a flow is in progress.
func (c *Client) MCPAuthURL(ctx context.Context, id, name string) (string, error) {
	q := url.Values{"name": []string{name}}
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/mcp/auth-url", id), q, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get MCP auth URL: %w", err)
	}
	var resp proto.MCPAuthResponse
	if err := decodeJSON(rsp, &resp, "failed to get MCP auth URL", "MCP auth URL"); err != nil {
		return "", err
	}
	return resp.AuthURL, nil
}

// MCPAuthenticate runs the OAuth flow for a named MCP server. The server's
// local browser is suppressed; the caller is responsible for surfacing the
// authorization URL (via polling [Client.MCPPendingAuth] / state events)
// and opening it on the user's machine. The call blocks until the flow
// completes, fails, or ctx is cancelled.
func (c *Client) MCPAuthenticate(ctx context.Context, id, name string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/mcp/auth", id), nil,
		jsonBody(proto.MCPNameRequest{Name: name}),
		http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to authenticate MCP: %w", err)
	}
	return okOrError(rsp, "failed to authenticate MCP")
}

// MCPReconnect restarts a named MCP server, clearing a session-scoped
// disable.
func (c *Client) MCPReconnect(ctx context.Context, id, name string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/mcp/reconnect", id), nil,
		jsonBody(proto.MCPNameRequest{Name: name}),
		http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to reconnect MCP: %w", err)
	}
	return okOrError(rsp, "failed to reconnect MCP")
}

// MCPDisableForSession disables a named MCP server for the rest of the
// server process without touching its configuration.
func (c *Client) MCPDisableForSession(ctx context.Context, id, name string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/mcp/disable", id), nil,
		jsonBody(proto.MCPNameRequest{Name: name}),
		http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to disable MCP: %w", err)
	}
	return okOrError(rsp, "failed to disable MCP")
}

// MCPRefreshPrompts refreshes prompts for a named MCP client.
func (c *Client) MCPRefreshPrompts(ctx context.Context, id, name string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/mcp/refresh-prompts", id), nil,
		jsonBody(struct {
			Name string `json:"name"`
		}{Name: name}),
		http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to refresh MCP prompts: %w", err)
	}
	if err := okOrError(rsp, "failed to refresh MCP prompts"); err != nil {
		return err
	}
	return nil
}

// MCPRefreshResources refreshes resources for a named MCP client.
func (c *Client) MCPRefreshResources(ctx context.Context, id, name string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/mcp/refresh-resources", id), nil,
		jsonBody(struct {
			Name string `json:"name"`
		}{Name: name}),
		http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to refresh MCP resources: %w", err)
	}
	if err := okOrError(rsp, "failed to refresh MCP resources"); err != nil {
		return err
	}
	return nil
}

// GetAgentSessionQueuedPrompts retrieves the number of queued prompts for a
// session.
func (c *Client) GetAgentSessionQueuedPrompts(ctx context.Context, id string, sessionID string) (int, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/agent/sessions/%s/prompts/queued", id, sessionID), nil, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to get session agent queued prompts: %w", err)
	}
	var count int
	if err := decodeJSON(rsp, &count, "failed to get session agent queued prompts", "session agent queued prompts"); err != nil {
		return 0, err
	}
	return count, nil
}

// ClearAgentSessionQueuedPrompts clears the queued prompts for a session.
func (c *Client) ClearAgentSessionQueuedPrompts(ctx context.Context, id string, sessionID string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/agent/sessions/%s/prompts/clear", id, sessionID), nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to clear session agent queued prompts: %w", err)
	}
	if err := okOrError(rsp, "failed to clear session agent queued prompts"); err != nil {
		return err
	}
	return nil
}

// GetAgentInfo retrieves the agent status for a workspace.
func (c *Client) GetAgentInfo(ctx context.Context, id string) (*proto.AgentInfo, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/agent", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get agent status: %w", err)
	}
	defer rsp.Body.Close()
	if err := checkStatus(rsp); err != nil {
		return nil, fmt.Errorf("failed to get agent status: %w", err)
	}
	var info proto.AgentInfo
	if err := json.NewDecoder(rsp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode agent status: %w", err)
	}
	return &info, nil
}

// UpdateAgent triggers an agent model update on the server.
func (c *Client) UpdateAgent(ctx context.Context, id string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/agent/update", id), nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to update agent: %w", err)
	}
	if err := okOrError(rsp, "failed to update agent"); err != nil {
		return err
	}
	return nil
}

// SendMessage sends a message to the agent for a workspace.
//
// When runID is non-empty it is echoed back on the resulting
// proto.RunComplete event, giving the caller a unique correlator
// for completion detection. Pass "" when the caller does not need
// to distinguish its own turn's terminal event from any concurrent
// turn on the same session (e.g. interactive TUI usage).
func (c *Client) SendMessage(ctx context.Context, id string, sessionID, runID, prompt string, attachments ...message.Attachment) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/agent", id), nil, jsonBody(proto.AgentMessage{
		SessionID:   sessionID,
		RunID:       runID,
		Prompt:      prompt,
		Attachments: proto.AttachmentsFromMessage(attachments),
	}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to send message to agent: %w", err)
	}
	defer rsp.Body.Close()
	if err := checkStatus(rsp, http.StatusOK, http.StatusAccepted); err != nil {
		return fmt.Errorf("failed to send message to agent: %w", err)
	}
	return nil
}

// decodeErrorMessage attempts to decode the response body as a
// proto.Error and returns its message. It returns an empty string
// when the body is empty or cannot be decoded into a proto.Error
// with a non-empty message, letting callers fall back to a
// status-only error.
func decodeErrorMessage(body io.Reader) string {
	var e proto.Error
	if err := json.NewDecoder(body).Decode(&e); err != nil {
		return ""
	}
	return e.Message
}

// RunShellCommand runs a shell command in the workspace without triggering the agent.
func (c *Client) RunShellCommand(ctx context.Context, id, sessionID, command string, termWidth int) (proto.ShellCommandResponse, error) {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/agent/sessions/%s/shell", id, sessionID), nil, jsonBody(proto.ShellCommandRequest{
		Command:   command,
		TermWidth: termWidth,
	}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return proto.ShellCommandResponse{}, fmt.Errorf("failed to run shell command: %w", err)
	}
	var resp proto.ShellCommandResponse
	if err := decodeJSON(rsp, &resp, "failed to run shell command", "shell command response"); err != nil {
		return proto.ShellCommandResponse{}, err
	}
	return resp, nil
}

// GetAgentSessionInfo retrieves the agent session info for a workspace.
func (c *Client) GetAgentSessionInfo(ctx context.Context, id string, sessionID string) (*proto.AgentSession, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/agent/sessions/%s", id, sessionID), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get session agent info: %w", err)
	}
	var info proto.AgentSession
	if err := decodeJSON(rsp, &info, "failed to get session agent info", "session agent info"); err != nil {
		return nil, err
	}
	return &info, nil
}

// AgentSummarizeSession requests a session summarization. instructions,
// when non-empty, steer the summary's focus (/compact input).
func (c *Client) AgentSummarizeSession(ctx context.Context, id string, sessionID string, instructions string) error {
	var body io.Reader
	if instructions != "" {
		payload, err := json.Marshal(proto.SummarizeRequest{Instructions: instructions})
		if err != nil {
			return fmt.Errorf("failed to summarize session: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/agent/sessions/%s/summarize", id, sessionID), nil, body, nil)
	if err != nil {
		return fmt.Errorf("failed to summarize session: %w", err)
	}
	if err := okOrError(rsp, "failed to summarize session"); err != nil {
		return err
	}
	return nil
}

// InitiateAgentProcessing triggers agent initialization on the server.
func (c *Client) InitiateAgentProcessing(ctx context.Context, id string, interactive bool) error {
	body := jsonBody(proto.AgentInitRequest{Interactive: interactive})
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/agent/init", id), nil, body, http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to initiate session agent processing: %w", err)
	}
	if err := okOrError(rsp, "failed to initiate session agent processing"); err != nil {
		return err
	}
	return nil
}

// ListMessages retrieves all messages for a session as proto types.
func (c *Client) ListMessages(ctx context.Context, id string, sessionID string) ([]proto.Message, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/sessions/%s/messages", id, sessionID), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}
	var msgs []proto.Message
	if err := decodeJSONAllowEmpty(rsp, &msgs, "failed to get messages", "messages"); err != nil {
		return nil, err
	}
	return msgs, nil
}

// GetSession retrieves a specific session as a proto type.
func (c *Client) GetSession(ctx context.Context, id string, sessionID string) (*proto.Session, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/sessions/%s", id, sessionID), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}
	var sess proto.Session
	if err := decodeJSON(rsp, &sess, "failed to get session", "session"); err != nil {
		return nil, err
	}
	return &sess, nil
}

// ListSessionHistoryFiles retrieves history files for a session as proto types.
func (c *Client) ListSessionHistoryFiles(ctx context.Context, id string, sessionID string) ([]proto.File, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/sessions/%s/history", id, sessionID), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get session history files: %w", err)
	}
	var files []proto.File
	if err := decodeJSON(rsp, &files, "failed to get session history files", "session history files"); err != nil {
		return nil, err
	}
	return files, nil
}

// CreateSession creates a new session in a workspace as a proto type.
func (c *Client) CreateSession(ctx context.Context, id string, title string) (*proto.Session, error) {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/sessions", id), nil, jsonBody(proto.Session{Title: title}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	var sess proto.Session
	if err := decodeJSON(rsp, &sess, "failed to create session", "session"); err != nil {
		return nil, err
	}
	return &sess, nil
}

// ListSessions lists all sessions in a workspace as proto types.
func (c *Client) ListSessions(ctx context.Context, id string) ([]proto.Session, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/sessions", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get sessions: %w", err)
	}
	var sessions []proto.Session
	if err := decodeJSON(rsp, &sessions, "failed to get sessions", "sessions"); err != nil {
		return nil, err
	}
	return sessions, nil
}

// AnswerQuestionBatch submits answers for a batch question on a
// workspace. Returns true if this call resolved the pending
// request, false if already resolved by another caller.
func (c *Client) AnswerQuestionBatch(ctx context.Context, id string, req proto.QuestionAnswer) (bool, error) {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/questions/answer", id), nil, jsonBody(req), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return false, fmt.Errorf("failed to answer question batch: %w", err)
	}
	var resp proto.QuestionAnswerResponse
	if err := decodeJSON(rsp, &resp, "failed to answer question batch", "answer question batch response"); err != nil {
		return false, err
	}
	return resp.Resolved, nil
}

// CancelQuestionBatch cancels the pending question batch on a
// workspace. Returns true if a question was cancelled, false if
// none was pending.
func (c *Client) CancelQuestionBatch(ctx context.Context, id string) (bool, error) {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/questions/cancel", id), nil, nil, http.Header{})
	if err != nil {
		return false, fmt.Errorf("failed to cancel question batch: %w", err)
	}
	var resp proto.QuestionAnswerResponse
	if err := decodeJSON(rsp, &resp, "failed to cancel question batch", "cancel question batch response"); err != nil {
		return false, err
	}
	return resp.Resolved, nil
}

// GetConfig retrieves the workspace-specific configuration.
func (c *Client) GetConfig(ctx context.Context, id string) (*config.Config, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/config", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get config: %w", err)
	}
	var cfg config.Config
	if err := decodeJSON(rsp, &cfg, "failed to get config", "config"); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func jsonBody(v any) *bytes.Buffer {
	b := new(bytes.Buffer)
	m, _ := json.Marshal(v)
	b.Write(m)
	return b
}

// SaveSession updates a session in a workspace, returning a proto type.
func (c *Client) SaveSession(ctx context.Context, id string, sess proto.Session) (*proto.Session, error) {
	rsp, err := c.put(ctx, fmt.Sprintf("/workspaces/%s/sessions/%s", id, sess.ID), nil, jsonBody(sess), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}
	var saved proto.Session
	if err := decodeJSON(rsp, &saved, "failed to save session", "session"); err != nil {
		return nil, err
	}
	return &saved, nil
}

// DeleteSession deletes a session from a workspace.
func (c *Client) DeleteSession(ctx context.Context, id string, sessionID string) error {
	rsp, err := c.delete(ctx, fmt.Sprintf("/workspaces/%s/sessions/%s", id, sessionID), nil, nil)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	if err := okOrError(rsp, "failed to delete session"); err != nil {
		return err
	}
	return nil
}

// ListUserMessages retrieves user-role messages for a session as proto types.
func (c *Client) ListUserMessages(ctx context.Context, id string, sessionID string) ([]proto.Message, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/sessions/%s/messages/user", id, sessionID), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get user messages: %w", err)
	}
	var msgs []proto.Message
	if err := decodeJSONAllowEmpty(rsp, &msgs, "failed to get user messages", "user messages"); err != nil {
		return nil, err
	}
	return msgs, nil
}

// ListAllUserMessages retrieves all user-role messages across sessions as proto types.
func (c *Client) ListAllUserMessages(ctx context.Context, id string) ([]proto.Message, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/messages/user", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get all user messages: %w", err)
	}
	var msgs []proto.Message
	if err := decodeJSONAllowEmpty(rsp, &msgs, "failed to get all user messages", "all user messages"); err != nil {
		return nil, err
	}
	return msgs, nil
}

// CancelAgentSession cancels an ongoing agent operation for a session.
func (c *Client) CancelAgentSession(ctx context.Context, id string, sessionID string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/agent/sessions/%s/cancel", id, sessionID), nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to cancel agent session: %w", err)
	}
	if err := okOrError(rsp, "failed to cancel agent session"); err != nil {
		return err
	}
	return nil
}

// CancelAgentSessionTurn interrupts the session's active run only;
// queued prompts survive and run once the interrupted turn unwinds.
func (c *Client) CancelAgentSessionTurn(ctx context.Context, id string, sessionID string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/agent/sessions/%s/cancel-turn", id, sessionID), nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to cancel agent session turn: %w", err)
	}
	if err := okOrError(rsp, "failed to cancel agent session turn"); err != nil {
		return err
	}
	return nil
}

// GetAgentSessionQueuedPromptsList retrieves the list of queued prompt
// strings for a session.
func (c *Client) GetAgentSessionQueuedPromptsList(ctx context.Context, id string, sessionID string) ([]string, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/agent/sessions/%s/prompts/list", id, sessionID), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get queued prompts list: %w", err)
	}
	var prompts []string
	if err := decodeJSON(rsp, &prompts, "failed to get queued prompts list", "queued prompts list"); err != nil {
		return nil, err
	}
	return prompts, nil
}

// GetDefaultSmallModel retrieves the default small model for a provider.
func (c *Client) GetDefaultSmallModel(ctx context.Context, id string, providerID string) (*config.SelectedModel, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/agent/default-small-model", id), url.Values{"provider_id": []string{providerID}}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get default small model: %w", err)
	}
	var model config.SelectedModel
	if err := decodeJSON(rsp, &model, "failed to get default small model", "default small model"); err != nil {
		return nil, err
	}
	return &model, nil
}

// FileTrackerRecordRead records a file read for a session.
func (c *Client) FileTrackerRecordRead(ctx context.Context, id string, sessionID, path string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/filetracker/read", id), nil, jsonBody(struct {
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
	}{SessionID: sessionID, Path: path}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to record file read: %w", err)
	}
	if err := okOrError(rsp, "failed to record file read"); err != nil {
		return err
	}
	return nil
}

// FileTrackerLastReadTime returns the last read time for a file in a
// session.
func (c *Client) FileTrackerLastReadTime(ctx context.Context, id string, sessionID, path string) (time.Time, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/filetracker/lastread", id), url.Values{
		"session_id": []string{sessionID},
		"path":       []string{path},
	}, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get last read time: %w", err)
	}
	var t time.Time
	if err := decodeJSON(rsp, &t, "failed to get last read time", "last read time"); err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// FileTrackerListReadFiles returns the list of read files for a session.
func (c *Client) FileTrackerListReadFiles(ctx context.Context, id string, sessionID string) ([]string, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/sessions/%s/filetracker/files", id, sessionID), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get read files: %w", err)
	}
	var files []string
	if err := decodeJSON(rsp, &files, "failed to get read files", "read files"); err != nil {
		return nil, err
	}
	return files, nil
}

// LSPStart starts an LSP server for a path.
func (c *Client) LSPStart(ctx context.Context, id string, path string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/lsps/start", id), nil, jsonBody(struct {
		Path string `json:"path"`
	}{Path: path}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to start LSP: %w", err)
	}
	if err := okOrError(rsp, "failed to start LSP"); err != nil {
		return err
	}
	return nil
}

// LSPStopAll stops all LSP servers for a workspace.
func (c *Client) LSPStopAll(ctx context.Context, id string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/lsps/stop", id), nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to stop LSPs: %w", err)
	}
	if err := okOrError(rsp, "failed to stop LSPs"); err != nil {
		return err
	}
	return nil
}

// LSPRestartSingle restarts a named running LSP server.
func (c *Client) LSPRestartSingle(ctx context.Context, id, name string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/lsps/restart", id), nil,
		jsonBody(proto.LSPNameRequest{Name: name}),
		http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to restart LSP: %w", err)
	}
	return okOrError(rsp, "failed to restart LSP")
}

// LSPSetSessionDisabled turns a named LSP server off (or back on) for
// the rest of the server process without touching its configuration.
func (c *Client) LSPSetSessionDisabled(ctx context.Context, id, name string, disabled bool) error {
	path := "enable"
	if disabled {
		path = "disable"
	}
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/lsps/%s", id, path), nil,
		jsonBody(proto.LSPNameRequest{Name: name}),
		http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to set LSP session state: %w", err)
	}
	return okOrError(rsp, "failed to set LSP session state")
}

// ListCheckpoints retrieves a session's rewind checkpoints.
func (c *Client) ListCheckpoints(ctx context.Context, id string, sessionID string) ([]proto.Checkpoint, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/sessions/%s/checkpoints", id, sessionID), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoints: %w", err)
	}
	var checkpoints []proto.Checkpoint
	if err := decodeJSONAllowEmpty(rsp, &checkpoints, "failed to get checkpoints", "checkpoints"); err != nil {
		return nil, err
	}
	return checkpoints, nil
}

// RewindSession rewinds a session to an earlier turn.
func (c *Client) RewindSession(ctx context.Context, id, sessionID, messageID, mode string) error {
	body := jsonBody(proto.RewindRequest{MessageID: messageID, Mode: mode})
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/sessions/%s/rewind", id, sessionID), nil, body, http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to rewind session: %w", err)
	}
	if err := okOrError(rsp, "failed to rewind session"); err != nil {
		return err
	}
	return nil
}
