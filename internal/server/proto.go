package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/stubbedev/harness/internal/backend"
	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/workspace"
)

type controllerV1 struct {
	backend *backend.Backend
	server  *Server
}

// handleGetHealth checks server health.
//
//	@Summary		Health check
//	@Tags			system
//	@Success		200
//	@Router			/health [get]
func (c *controllerV1) handleGetHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleGetVersion returns server version information.
//
//	@Summary		Get server version
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	proto.VersionInfo
//	@Router			/version [get]
func (c *controllerV1) handleGetVersion(w http.ResponseWriter, _ *http.Request) {
	jsonEncode(w, c.backend.VersionInfo())
}

// handlePostControl sends a control command to the server.
//
//	@Summary		Send server control command
//	@Tags			system
//	@Accept			json
//	@Param			request	body	proto.ServerControl	true	"Control command (e.g. shutdown, shutdown_if_idle)"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		409	{object}	proto.Error
//	@Router			/control [post]
func (c *controllerV1) handlePostControl(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBody[proto.ServerControl](c, w, r, false)
	if !ok {
		return
	}

	switch req.Command {
	case proto.ServerControlShutdown, proto.ServerControlShutdownIfIdle:
		// Both spellings are conditional. Only the backend can rule on
		// idleness without racing a session that arrives between a
		// client's own check and its request, and guarding the plain
		// command too means clients predating the check cannot take live
		// sessions down either.
		if !c.backend.ShutdownIfIdle() {
			c.handleError(w, r, backend.ErrServerNotIdle)
			return
		}
	default:
		c.handleError(w, r, fmt.Errorf("%w: %q", backend.ErrUnknownCommand, req.Command))
		return
	}
}

// handleGetWorkspaces lists all workspaces.
//
//	@Summary		List workspaces
//	@Tags			workspaces
//	@Produce		json
//	@Success		200	{array}		proto.Workspace
//	@Router			/workspaces [get]
func (c *controllerV1) handleGetWorkspaces(w http.ResponseWriter, _ *http.Request) {
	jsonEncode(w, c.backend.ListWorkspaces())
}

// handleGetWorkspace returns a single workspace by ID.
//
//	@Summary		Get workspace
//	@Tags			workspaces
//	@Produce		json
//	@Param			id	path		string	true	"Workspace ID"
//	@Success		200	{object}	proto.Workspace
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id} [get]
func (c *controllerV1) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := c.backend.GetWorkspaceProto(r.PathValue("id"))
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, ws)
}

// handlePostWorkspaces creates a new workspace.
//
//	@Summary		Create workspace
//	@Tags			workspaces
//	@Accept			json
//	@Produce		json
//	@Param			request	body		proto.Workspace	true	"Workspace creation params"
//	@Success		200		{object}	proto.Workspace
//	@Failure		400		{object}	proto.Error
//	@Failure		500		{object}	proto.Error
//	@Router			/workspaces [post]
func (c *controllerV1) handlePostWorkspaces(w http.ResponseWriter, r *http.Request) {
	args, ok := decodeBody[proto.Workspace](c, w, r, false)
	if !ok {
		return
	}
	_, result, err := c.backend.CreateWorkspace(args)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, result)
}

// requireClientID reads the client_id query parameter and validates it
// as a UUID. On failure it writes a 400 and returns false.
func (c *controllerV1) requireClientID(w http.ResponseWriter, r *http.Request) (string, bool) {
	cid := r.URL.Query().Get("client_id")
	if cid == "" {
		c.server.logError(r, "Missing client_id query parameter")
		jsonError(w, http.StatusBadRequest, "client_id is required")
		return "", false
	}
	if _, err := uuid.Parse(cid); err != nil {
		c.server.logError(r, "Invalid client_id", "error", err)
		jsonError(w, http.StatusBadRequest, "client_id is not a valid UUID")
		return "", false
	}
	return cid, true
}

// handlePostWorkspaceCurrentSession records the calling client's
// current session selection for the workspace. An empty session_id
// clears the entry (e.g. the client is on the landing screen).
//
//	@Summary		Set current session for a client
//	@Tags			workspaces
//	@Accept			json
//	@Produce		json
//	@Param			id			path	string					true	"Workspace ID"
//	@Param			client_id	query	string					true	"Client ID (UUID)"
//	@Param			request		body	proto.CurrentSession	true	"Current session selection"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Router			/workspaces/{id}/current-session [post]
func (c *controllerV1) handlePostWorkspaceCurrentSession(w http.ResponseWriter, r *http.Request) {
	clientID, ok := c.requireClientID(w, r)
	if !ok {
		return
	}
	req, ok := decodeBody[proto.CurrentSession](c, w, r, false)
	if !ok {
		return
	}
	if err := c.backend.SetCurrentSession(r.PathValue("id"), clientID, req.SessionID); err != nil {
		c.handleError(w, r, err)
	}
}

// handleDeleteClient retires a client, releasing every claim it holds.
//
//	@Summary		Retire a client
//	@Tags			system
//	@Param			client_id	path	string	true	"Client ID (UUID)"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Router			/clients/{client_id} [delete]
func (c *controllerV1) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	if err := c.backend.RetireClient(r.PathValue("client_id")); err != nil {
		c.handleError(w, r, err)
	}
}

// handleDeleteWorkspaces deletes a workspace.
//
//	@Summary		Delete workspace
//	@Tags			workspaces
//	@Param			id	path	string	true	"Workspace ID"
//	@Success		200
//	@Failure		404	{object}	proto.Error
//	@Router			/workspaces/{id} [delete]
func (c *controllerV1) handleDeleteWorkspaces(w http.ResponseWriter, r *http.Request) {
	clientID, ok := c.requireClientID(w, r)
	if !ok {
		return
	}
	if err := c.backend.DeleteWorkspace(r.PathValue("id"), clientID); err != nil {
		c.handleError(w, r, err)
	}
}

// handleGetWorkspaceConfig returns workspace configuration.
//
//	@Summary		Get workspace config
//	@Tags			workspaces
//	@Produce		json
//	@Param			id	path		string	true	"Workspace ID"
//	@Success		200	{object}	object
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/config [get]
func (c *controllerV1) handleGetWorkspaceConfig(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return ws.Cfg.Config(), nil
	})
}

// handleGetWorkspaceEvents streams workspace events as Server-Sent Events.
//
//	@Summary		Stream workspace events (SSE)
//	@Tags			workspaces
//	@Produce		text/event-stream
//	@Param			id	path	string	true	"Workspace ID"
//	@Success		200
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/events [get]
func (c *controllerV1) handleGetWorkspaceEvents(w http.ResponseWriter, r *http.Request) {
	flusher := http.NewResponseController(w)
	id := r.PathValue("id")
	clientID, ok := c.requireClientID(w, r)
	if !ok {
		return
	}
	// Subscribe to the event broker BEFORE attaching the client.
	// AttachClient bumps the stream count that observers use to
	// detect a live subscriber; subscribing first guarantees that
	// once a client appears attached, any published event is
	// delivered rather than dropped on a not-yet-registered stream.
	events, err := c.backend.SubscribeEvents(r.Context(), id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	if err := c.backend.AttachClient(id, clientID); err != nil {
		c.handleError(w, r, err)
		return
	}
	defer c.backend.DetachClient(id, clientID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Flush headers immediately so clients see the 200 response
	// before any events arrive. Without this, a quiet workspace
	// keeps the client's SubscribeEvents call blocked on the
	// initial RoundTrip.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			c.server.logDebug(r, "Stopping event stream")
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			wrapped := wrapEvent(ev.Payload)
			if wrapped == nil {
				continue
			}
			data, err := json.Marshal(wrapped)
			if err != nil {
				c.server.logError(r, "Failed to marshal event", "error", err)
				continue
			}

			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleGetWorkspaceLSPs lists LSP clients for a workspace.
//
//	@Summary		List LSP clients
//	@Tags			lsp
//	@Produce		json
//	@Param			id	path		string							true	"Workspace ID"
//	@Success		200	{object}	map[string]proto.LSPClientInfo
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/lsps [get]
func (c *controllerV1) handleGetWorkspaceLSPs(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return mapValues(ws.Ops().LSPGetStates(), func(v workspace.LSPClientInfo) proto.LSPClientInfo {
			return proto.LSPClientInfo(v)
		}), nil
	})
}

// handleGetWorkspaceLSPDiagnostics returns diagnostics for an LSP client.
//
//	@Summary		Get LSP diagnostics
//	@Tags			lsp
//	@Produce		json
//	@Param			id	path		string	true	"Workspace ID"
//	@Param			lsp	path		string	true	"LSP client name"
//	@Success		200	{object}	object
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/lsps/{lsp}/diagnostics [get]
func (c *controllerV1) handleGetWorkspaceLSPDiagnostics(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return c.backend.GetLSPDiagnostics(ws.ID, r.PathValue("lsp"))
	})
}

// handleGetWorkspaceSessions lists sessions for a workspace.
//
//	@Summary		List sessions
//	@Tags			sessions
//	@Produce		json
//	@Param			id	path		string			true	"Workspace ID"
//	@Success		200	{array}		proto.Session
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/sessions [get]
func (c *controllerV1) handleGetWorkspaceSessions(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		sessions, err := ws.Ops().ListSessions(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]proto.Session, len(sessions))
		for i, s := range sessions {
			result[i] = sessionOut(ws, s)
		}
		return result, nil
	})
}

// handlePostWorkspaceSessions creates a new session in a workspace.
//
//	@Summary		Create session
//	@Tags			sessions
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string			true	"Workspace ID"
//	@Param			request	body		proto.Session	true	"Session creation params (title)"
//	@Success		200		{object}	proto.Session
//	@Failure		400		{object}	proto.Error
//	@Failure		404		{object}	proto.Error
//	@Failure		500		{object}	proto.Error
//	@Router			/workspaces/{id}/sessions [post]
func (c *controllerV1) handlePostWorkspaceSessions(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.Session) (any, error) {
		s, err := ws.Ops().CreateSession(ctx, req.Title)
		return sessionResult(ws, s, err)
	})
}

// handleGetWorkspaceSession returns a single session.
//
//	@Summary		Get session
//	@Tags			sessions
//	@Produce		json
//	@Param			id	path		string	true	"Workspace ID"
//	@Param			sid	path		string	true	"Session ID"
//	@Success		200	{object}	proto.Session
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/sessions/{sid} [get]
func (c *controllerV1) handleGetWorkspaceSession(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		s, err := ws.Ops().GetSession(ctx, r.PathValue("sid"))
		return sessionResult(ws, s, err)
	})
}

// handleGetWorkspaceSessionHistory returns the history for a session.
//
//	@Summary		Get session history
//	@Tags			sessions
//	@Produce		json
//	@Param			id	path		string		true	"Workspace ID"
//	@Param			sid	path		string		true	"Session ID"
//	@Success		200	{array}		proto.File
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/sessions/{sid}/history [get]
func (c *controllerV1) handleGetWorkspaceSessionHistory(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		files, err := ws.Ops().ListSessionHistory(ctx, r.PathValue("sid"))
		return proto.FilesFromDomain(files), err
	})
}

// handleGetWorkspaceSessionMessages returns all messages for a session.
//
//	@Summary		Get session messages
//	@Tags			sessions
//	@Produce		json
//	@Param			id	path		string			true	"Workspace ID"
//	@Param			sid	path		string			true	"Session ID"
//	@Success		200	{array}		proto.Message
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/sessions/{sid}/messages [get]
func (c *controllerV1) handleGetWorkspaceSessionMessages(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		msgs, err := ws.Ops().ListMessages(ctx, r.PathValue("sid"))
		return proto.MessagesFromDomain(msgs), err
	})
}

// handlePutWorkspaceSession renames a session. Only the title is
// written; the stored usage, summary and compaction fields are left
// untouched.
//
//	@Summary		Rename session
//	@Tags			sessions
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string						true	"Workspace ID"
//	@Param			sid		path		string						true	"Session ID"
//	@Param			request	body		proto.SessionRenameRequest	true	"New title"
//	@Success		200		{object}	proto.Session
//	@Failure		400		{object}	proto.Error
//	@Failure		404		{object}	proto.Error
//	@Failure		500		{object}	proto.Error
//	@Router			/workspaces/{id}/sessions/{sid} [put]
func (c *controllerV1) handlePutWorkspaceSession(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.SessionRenameRequest) (any, error) {
		sid := r.PathValue("sid")
		ops := ws.Ops()
		if err := ops.RenameSession(ctx, sid, req.Title); err != nil {
			return nil, err
		}
		s, err := ops.GetSession(ctx, sid)
		return sessionResult(ws, s, err)
	})
}

// handleDeleteWorkspaceSession deletes a session.
//
//	@Summary		Delete session
//	@Tags			sessions
//	@Param			id	path	string	true	"Workspace ID"
//	@Param			sid	path	string	true	"Session ID"
//	@Success		200
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/sessions/{sid} [delete]
func (c *controllerV1) handleDeleteWorkspaceSession(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		return done(ws.Ops().DeleteSession(ctx, r.PathValue("sid")))
	})
}

// handleGetWorkspaceSessionUserMessages returns user messages for a session.
//
//	@Summary		Get user messages for session
//	@Tags			sessions
//	@Produce		json
//	@Param			id	path		string			true	"Workspace ID"
//	@Param			sid	path		string			true	"Session ID"
//	@Success		200	{array}		proto.Message
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/sessions/{sid}/messages/user [get]
func (c *controllerV1) handleGetWorkspaceSessionUserMessages(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		msgs, err := ws.Ops().ListUserMessages(ctx, r.PathValue("sid"))
		return proto.MessagesFromDomain(msgs), err
	})
}

// handleGetWorkspaceAllUserMessages returns all user messages across sessions.
//
//	@Summary		Get all user messages for workspace
//	@Tags			workspaces
//	@Produce		json
//	@Param			id	path		string			true	"Workspace ID"
//	@Success		200	{array}		proto.Message
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/messages/user [get]
func (c *controllerV1) handleGetWorkspaceAllUserMessages(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		msgs, err := ws.Ops().ListAllUserMessages(ctx)
		return proto.MessagesFromDomain(msgs), err
	})
}

// handleGetWorkspaceSessionFileTrackerFiles lists files read in a session.
//
//	@Summary		List tracked files for session
//	@Tags			filetracker
//	@Produce		json
//	@Param			id	path		string		true	"Workspace ID"
//	@Param			sid	path		string		true	"Session ID"
//	@Success		200	{array}		string
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/sessions/{sid}/filetracker/files [get]
func (c *controllerV1) handleGetWorkspaceSessionFileTrackerFiles(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		return ws.Ops().FileTrackerListReadFiles(ctx, r.PathValue("sid"))
	})
}

// handlePostWorkspaceFileTrackerRead records a file read event.
//
//	@Summary		Record file read
//	@Tags			filetracker
//	@Accept			json
//	@Param			id		path	string							true	"Workspace ID"
//	@Param			request	body	proto.FileTrackerReadRequest	true	"File tracker read request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/filetracker/read [post]
func (c *controllerV1) handlePostWorkspaceFileTrackerRead(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.FileTrackerReadRequest) (any, error) {
		ws.Ops().FileTrackerRecordRead(ctx, req.SessionID, req.Path)
		return nil, nil
	})
}

// handleGetWorkspaceFileTrackerLastRead returns the last read time for a file.
//
//	@Summary		Get last read time for file
//	@Tags			filetracker
//	@Produce		json
//	@Param			id			path		string	true	"Workspace ID"
//	@Param			session_id	query		string	false	"Session ID"
//	@Param			path		query		string	true	"File path"
//	@Success		200			{object}	object
//	@Failure		404			{object}	proto.Error
//	@Failure		500			{object}	proto.Error
//	@Router			/workspaces/{id}/filetracker/lastread [get]
func (c *controllerV1) handleGetWorkspaceFileTrackerLastRead(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		q := r.URL.Query()
		return ws.Ops().FileTrackerLastReadTime(ctx, q.Get("session_id"), q.Get("path")), nil
	})
}

// handlePostWorkspaceLSPStart starts an LSP server for a path.
//
//	@Summary		Start LSP server
//	@Tags			lsp
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.LSPStartRequest	true	"LSP start request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/lsps/start [post]
func (c *controllerV1) handlePostWorkspaceLSPStart(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.LSPStartRequest) (any, error) {
		ws.Ops().LSPStart(ctx, req.Path)
		return nil, nil
	})
}

// handlePostWorkspaceLSPStopAll stops all LSP servers.
//
//	@Summary		Stop all LSP servers
//	@Tags			lsp
//	@Param			id	path	string	true	"Workspace ID"
//	@Success		200
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/lsps/stop [post]
func (c *controllerV1) handlePostWorkspaceLSPStopAll(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		ws.Ops().LSPStopAll(ctx)
		return nil, nil
	})
}

// handlePostWorkspaceLSPRestart restarts a named running LSP server.
//
//	@Summary		Restart an LSP server
//	@Tags			lsp
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.LSPNameRequest	true	"LSP name request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/lsps/restart [post]
func (c *controllerV1) handlePostWorkspaceLSPRestart(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.LSPNameRequest) (any, error) {
		return done(ws.Ops().LSPRestartSingle(ctx, req.Name))
	})
}

// handlePostWorkspaceLSPDisable turns a named LSP server off for the
// rest of the process without touching its configuration.
//
//	@Summary		Disable an LSP server for this session
//	@Tags			lsp
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.LSPNameRequest	true	"LSP name request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/lsps/disable [post]
func (c *controllerV1) handlePostWorkspaceLSPDisable(w http.ResponseWriter, r *http.Request) {
	c.handleLSPSessionDisabled(w, r, true)
}

// handlePostWorkspaceLSPEnable turns a session-disabled LSP server
// back on.
//
//	@Summary		Enable a session-disabled LSP server
//	@Tags			lsp
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.LSPNameRequest	true	"LSP name request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/lsps/enable [post]
func (c *controllerV1) handlePostWorkspaceLSPEnable(w http.ResponseWriter, r *http.Request) {
	c.handleLSPSessionDisabled(w, r, false)
}

// handleLSPSessionDisabled is the shared body of the LSP disable and
// enable endpoints.
func (c *controllerV1) handleLSPSessionDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.LSPNameRequest) (any, error) {
		return done(ws.Ops().LSPSetSessionDisabled(ctx, req.Name, disabled))
	})
}

// handleGetWorkspaceAgent returns agent info for a workspace.
//
//	@Summary		Get agent info
//	@Tags			agent
//	@Produce		json
//	@Param			id	path		string			true	"Workspace ID"
//	@Success		200	{object}	proto.AgentInfo
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent [get]
func (c *controllerV1) handleGetWorkspaceAgent(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		ops := ws.Ops()
		if !ops.AgentIsReady() {
			return proto.AgentInfo{}, nil
		}
		m := ops.AgentModel()
		return proto.AgentInfo{
			Model:    m.CatalogCfg,
			ModelCfg: m.ModelCfg,
			IsBusy:   ops.AgentIsBusy(),
			IsReady:  true,
		}, nil
	})
}

// handlePostWorkspaceAgent sends a message to the agent.
//
//	@Summary		Send message to agent
//	@Tags			agent
//	@Accept			json
//	@Param			id		path	string				true	"Workspace ID"
//	@Param			request	body	proto.AgentMessage	true	"Agent message"
//	@Success		202
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		409	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent [post]
func (c *controllerV1) handlePostWorkspaceAgent(w http.ResponseWriter, r *http.Request) {
	msg, ok := decodeBody[proto.AgentMessage](c, w, r, false)
	if !ok {
		return
	}
	// The run's lifetime is detached from the prompting client's HTTP
	// request: SendMessage validates and accepts the prompt, dispatches
	// the run on a goroutine bound to the workspace context, and returns
	// immediately. A dropping its TCP connection (network blip, TUI
	// restart) or B canceling the session via the explicit cancel
	// endpoint can no longer tear down a turn that other subscribed
	// clients are still watching. Only the explicit cancel endpoint
	// should be able to end a run.
	c.serveStatus(w, r, http.StatusAccepted, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return done(c.backend.SendMessage(ws.ID, msg))
	})
}

// handlePostWorkspaceAgentInit initializes the agent for a workspace.
//
//	@Summary		Initialize agent
//	@Tags			agent
//	@Param			id	path	string	true	"Workspace ID"
//	@Success		200
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent/init [post]
func (c *controllerV1) handlePostWorkspaceAgentInit(w http.ResponseWriter, r *http.Request) {
	serveOptionalBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.AgentInitRequest) (any, error) {
		if req.Interactive {
			return done(ws.Ops().InitCoderAgent(ctx))
		}
		return done(ws.Ops().InitCoderAgentNonInteractive(ctx))
	})
}

// handlePostWorkspaceAgentUpdate updates the agent for a workspace.
//
//	@Summary		Update agent
//	@Tags			agent
//	@Param			id	path	string	true	"Workspace ID"
//	@Success		200
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent/update [post]
func (c *controllerV1) handlePostWorkspaceAgentUpdate(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		return done(ws.Ops().UpdateAgentModel(ctx))
	})
}

// handleGetWorkspaceAgentSession returns a specific agent session.
//
//	@Summary		Get agent session
//	@Tags			agent
//	@Produce		json
//	@Param			id	path		string				true	"Workspace ID"
//	@Param			sid	path		string				true	"Session ID"
//	@Success		200	{object}	proto.AgentSession
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent/sessions/{sid} [get]
func (c *controllerV1) handleGetWorkspaceAgentSession(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		s, err := ws.Ops().GetSession(ctx, r.PathValue("sid"))
		if err != nil {
			return nil, err
		}
		out := sessionOut(ws, s)
		return proto.AgentSession{Session: out, IsBusy: out.IsBusy}, nil
	})
}

// handlePostWorkspaceAgentSessionCancel cancels a running agent session.
//
//	@Summary		Cancel agent session
//	@Tags			agent
//	@Param			id	path	string	true	"Workspace ID"
//	@Param			sid	path	string	true	"Session ID"
//	@Success		200
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent/sessions/{sid}/cancel [post]
func (c *controllerV1) handlePostWorkspaceAgentSessionCancel(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		ws.Ops().AgentCancel(r.PathValue("sid"))
		return nil, nil
	})
}

// handlePostWorkspaceAgentSessionCancelTurn interrupts the session's
// active run only; queued prompts survive and run once the interrupted
// turn unwinds. It backs the TUI's escape key.
//
//	@Summary		Cancel the session's active agent turn
//	@Tags			agent
//	@Param			id	path	string	true	"Workspace ID"
//	@Param			sid	path	string	true	"Session ID"
//	@Success		200
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent/sessions/{sid}/cancel-turn [post]
func (c *controllerV1) handlePostWorkspaceAgentSessionCancelTurn(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		ws.Ops().AgentCancelTurn(r.PathValue("sid"))
		return nil, nil
	})
}

// handleGetWorkspaceAgentSessionPromptQueued returns whether a queued prompt exists.
//
//	@Summary		Get queued prompt status
//	@Tags			agent
//	@Produce		json
//	@Param			id	path		string	true	"Workspace ID"
//	@Param			sid	path		string	true	"Session ID"
//	@Success		200	{object}	object
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent/sessions/{sid}/prompts/queued [get]
func (c *controllerV1) handleGetWorkspaceAgentSessionPromptQueued(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return ws.Ops().AgentQueuedPrompts(r.PathValue("sid")), nil
	})
}

// handlePostWorkspaceAgentSessionPromptClear clears the prompt queue for a session.
//
//	@Summary		Clear prompt queue
//	@Tags			agent
//	@Param			id	path	string	true	"Workspace ID"
//	@Param			sid	path	string	true	"Session ID"
//	@Success		200
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent/sessions/{sid}/prompts/clear [post]
func (c *controllerV1) handlePostWorkspaceAgentSessionPromptClear(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		ws.Ops().AgentClearQueue(r.PathValue("sid"))
		return nil, nil
	})
}

// handlePostWorkspaceAgentSessionSummarize summarizes a session.
//
//	@Summary		Summarize session
//	@Tags			agent
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			sid		path	string					true	"Session ID"
//	@Param			request	body	proto.SummarizeRequest	false	"Focus instructions"
//	@Success		200
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent/sessions/{sid}/summarize [post]
func (c *controllerV1) handlePostWorkspaceAgentSessionSummarize(w http.ResponseWriter, r *http.Request) {
	// The body carries optional focus instructions from /compact.
	serveOptionalBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.SummarizeRequest) (any, error) {
		return done(ws.Ops().AgentSummarize(ctx, r.PathValue("sid"), req.Instructions))
	})
}

// handlePostWorkspaceAgentSessionShell runs a shell command in the workspace.
//
//	@Summary		Run shell command
//	@Tags			agent
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string						true	"Workspace ID"
//	@Param			sid		path		string						true	"Session ID"
//	@Param			request	body		proto.ShellCommandRequest	true	"Shell command"
//	@Success		200		{object}	proto.ShellCommandResponse
//	@Failure		400		{object}	proto.Error
//	@Failure		404		{object}	proto.Error
//	@Failure		500		{object}	proto.Error
//	@Router			/workspaces/{id}/agent/sessions/{sid}/shell [post]
func (c *controllerV1) handlePostWorkspaceAgentSessionShell(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.ShellCommandRequest) (any, error) {
		return ws.Ops().AgentRunShellCommand(ctx, r.PathValue("sid"), req.Command, req.TermWidth, nil, req.IsFirstMessage)
	})
}

// handleGetWorkspaceAgentSessionPromptList returns the list of queued prompts.
//
//	@Summary		List queued prompts
//	@Tags			agent
//	@Produce		json
//	@Param			id	path		string		true	"Workspace ID"
//	@Param			sid	path		string		true	"Session ID"
//	@Success		200	{array}		string
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/agent/sessions/{sid}/prompts/list [get]
func (c *controllerV1) handleGetWorkspaceAgentSessionPromptList(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return ws.Ops().AgentQueuedPromptsList(r.PathValue("sid")), nil
	})
}

// handleGetWorkspaceAgentDefaultSmallModel returns the default small model for a provider.
//
//	@Summary		Get default small model
//	@Tags			agent
//	@Produce		json
//	@Param			id			path		string	true	"Workspace ID"
//	@Param			provider_id	query		string	false	"Provider ID"
//	@Success		200			{object}	object
//	@Failure		404			{object}	proto.Error
//	@Failure		500			{object}	proto.Error
//	@Router			/workspaces/{id}/agent/default-small-model [get]
func (c *controllerV1) handleGetWorkspaceAgentDefaultSmallModel(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return ws.Ops().GetDefaultSmallModel(r.URL.Query().Get("provider_id")), nil
	})
}

// handlePostWorkspaceQuestionsAnswer submits answers for a batch question.
//
//	@Summary		Answer question batch
//	@Tags			questions
//	@Accept			json
//	@Param			id		path	string						true	"Workspace ID"
//	@Param			request	body	proto.QuestionAnswer	true	"Question batch answer"
//	@Success		200	{object}	proto.QuestionAnswerResponse
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/questions/answer [post]
func (c *controllerV1) handlePostWorkspaceQuestionsAnswer(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(_ context.Context, ws *backend.Workspace, req proto.QuestionAnswer) (any, error) {
		resolved := ws.Ops().QuestionAnswer(proto.QuestionResponsesToDomain(req.Responses))
		return proto.QuestionAnswerResponse{Resolved: resolved}, nil
	})
}

// handlePostWorkspaceQuestionsCancel cancels the pending question
// batch for a workspace.
//
//	@Summary		Cancel question batch
//	@Tags			questions
//	@Param			id	path	string	true	"Workspace ID"
//	@Success		200	{object}	proto.QuestionAnswerResponse
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/questions/cancel [post]
func (c *controllerV1) handlePostWorkspaceQuestionsCancel(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return proto.QuestionAnswerResponse{Resolved: ws.Ops().QuestionCancel()}, nil
	})
}

// handleGetWorkspaceSessionCheckpoints returns a session's rewind
// checkpoints.
//
//	@Summary		Get session checkpoints
//	@Tags			sessions
//	@Produce		json
//	@Param			id	path		string	true	"Workspace ID"
//	@Param			sid	path		string	true	"Session ID"
//	@Success		200	{array}		proto.Checkpoint
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/sessions/{sid}/checkpoints [get]
func (c *controllerV1) handleGetWorkspaceSessionCheckpoints(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		cps, err := ws.Ops().ListCheckpoints(ctx, r.PathValue("sid"))
		return proto.CheckpointsFromDomain(cps), err
	})
}

// handlePostWorkspaceSessionRewind rewinds a session to an earlier
// turn.
//
//	@Summary		Rewind session
//	@Tags			sessions
//	@Accept			json
//	@Param			id		path	string				true	"Workspace ID"
//	@Param			sid		path	string				true	"Session ID"
//	@Param			request	body	proto.RewindRequest	true	"Rewind request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		409	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/sessions/{sid}/rewind [post]
func (c *controllerV1) handlePostWorkspaceSessionRewind(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.RewindRequest) (any, error) {
		return done(ws.Ops().Rewind(ctx, r.PathValue("sid"), req.MessageID, checkpoints.Mode(req.Mode)))
	})
}

// errorClass is the response an error maps to.
type errorClass struct {
	status int
	code   proto.ErrorCode
}

var (
	classWorkspaceGone = errorClass{http.StatusNotFound, proto.ErrorCodeWorkspaceNotFound}
	classNotFound      = errorClass{http.StatusNotFound, proto.ErrorCodeNotFound}
	classInvalid       = errorClass{http.StatusBadRequest, proto.ErrorCodeInvalidArgument}
	classConflict      = errorClass{http.StatusConflict, proto.ErrorCodeConflict}
	classUnavailable   = errorClass{http.StatusServiceUnavailable, proto.ErrorCodeUnavailable}
	classInternal      = errorClass{http.StatusInternalServerError, proto.ErrorCodeInternal}
)

// errorClasses maps backend sentinels to responses; the first match
// wins and anything unlisted is an internal error.
//
// context.Canceled from an agent run never reaches here: SendMessage
// answers 202 before the run starts, and the run reports cancellation
// over the event stream.
var errorClasses = []struct {
	err   error
	class errorClass
}{
	{backend.ErrWorkspaceNotFound, classWorkspaceGone},
	{backend.ErrLSPClientNotFound, classNotFound},
	{sql.ErrNoRows, classNotFound},
	{backend.ErrAgentNotInitialized, classInvalid},
	{backend.ErrPathRequired, classInvalid},
	{backend.ErrInvalidPermissionAction, classInvalid},
	{backend.ErrUnknownCommand, classInvalid},
	{backend.ErrInvalidClientID, classInvalid},
	{backend.ErrInvalidWorkspacePath, classInvalid},
	{backend.ErrInvalidSessionID, classInvalid},
	{backend.ErrInvalidRunID, classInvalid},
	{backend.ErrInvalidDataDir, classInvalid},
	{backend.ErrInvalidArgument, classInvalid},
	// 409, not 404: the workspace exists, the caller just has no live
	// stream yet.
	{backend.ErrClientNotAttached, classConflict},
	{backend.ErrWorkspaceClosing, classConflict},
	{backend.ErrServerNotIdle, classConflict},
	{backend.ErrClientRetired, classConflict},
	{backend.ErrSessionBusy, classConflict},
	{backend.ErrChannelOptInMismatch, classConflict},
	// 503, not 409: the request is not wrong, this process is just
	// leaving. Clients retry against its replacement.
	{backend.ErrServerShuttingDown, classUnavailable},
}

func classifyError(err error) errorClass {
	for _, e := range errorClasses {
		if errors.Is(err, e.err) {
			return e.class
		}
	}
	return classInternal
}

// handleError maps an error to its status and code and writes the JSON
// error response. Only server-side failures are logged at error level.
func (c *controllerV1) handleError(w http.ResponseWriter, r *http.Request, err error) {
	class := classifyError(err)
	if class.status >= http.StatusInternalServerError {
		c.server.logError(r, err.Error())
	} else {
		c.server.logDebug(r, err.Error())
	}
	writeError(w, class.status, class.code, err.Error())
}

func jsonEncode(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// jsonError writes an error response whose code follows from status.
func jsonError(w http.ResponseWriter, status int, message string) {
	code := proto.ErrorCodeInternal
	switch status {
	case http.StatusBadRequest:
		code = proto.ErrorCodeInvalidArgument
	case http.StatusNotFound:
		code = proto.ErrorCodeNotFound
	case http.StatusConflict:
		code = proto.ErrorCodeConflict
	case http.StatusServiceUnavailable:
		code = proto.ErrorCodeUnavailable
	}
	writeError(w, status, code, message)
}

func writeError(w http.ResponseWriter, status int, code proto.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(proto.Error{Message: message, Code: code})
}

// mapValues converts every value of a map with f.
func mapValues[K comparable, V, U any](in map[K]V, f func(V) U) map[K]U {
	out := make(map[K]U, len(in))
	for k, v := range in {
		out[k] = f(v)
	}
	return out
}
