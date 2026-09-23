package backend

import (
	"context"
	"fmt"

	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/session"
)

// CreateSession creates a new session in the given workspace.
func (b *Backend) CreateSession(ctx context.Context, workspaceID, title string) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.Sessions.Create(ctx, title)
}

// GetSession retrieves a session by workspace and session ID.
func (b *Backend) GetSession(ctx context.Context, workspaceID, sessionID string) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.Sessions.Get(ctx, sessionID)
}

// ListSessions returns all sessions in the given workspace.
func (b *Backend) ListSessions(ctx context.Context, workspaceID string) ([]session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Sessions.List(ctx)
}

// GetAgentSession returns session metadata with the agent's busy
// status.
func (b *Backend) GetAgentSession(ctx context.Context, workspaceID, sessionID string) (proto.AgentSession, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return proto.AgentSession{}, err
	}

	se, err := ws.Sessions.Get(ctx, sessionID)
	if err != nil {
		return proto.AgentSession{}, err
	}

	var isSessionBusy bool
	if ws.AgentCoordinator != nil {
		isSessionBusy = ws.AgentCoordinator.IsSessionBusy(sessionID)
	}

	return proto.AgentSession{
		ID:     se.ID,
		Title:  se.Title,
		IsBusy: isSessionBusy,
	}, nil
}

// ListSessionMessages returns all messages for a session.
func (b *Backend) ListSessionMessages(ctx context.Context, workspaceID, sessionID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	// Drain debounced updates so HTTP clients (and the TUI on session
	// switch) observe the latest in-memory state rather than racing the
	// debounce timer in message.Service.
	if err := ws.Messages.FlushAll(ctx); err != nil {
		return nil, err
	}
	return ws.Messages.List(ctx, sessionID)
}

// ListSessionHistory returns the history items for a session, including
// files edited by its direct child (subagent) sessions.
func (b *Backend) ListSessionHistory(ctx context.Context, workspaceID, sessionID string) ([]history.File, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.ListSessionHistory(ctx, sessionID)
}

// RenameSession changes only the title of a session in the given
// workspace and returns the stored session.
func (b *Backend) RenameSession(ctx context.Context, workspaceID, sessionID, title string) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}
	if err := ws.Sessions.Rename(ctx, sessionID, title); err != nil {
		return session.Session{}, err
	}
	return ws.Sessions.Get(ctx, sessionID)
}

// DeleteSession deletes a session from the given workspace, along
// with the snapshot objects its turns accumulated.
func (b *Backend) DeleteSession(ctx context.Context, workspaceID, sessionID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if err := ws.Sessions.Delete(ctx, sessionID); err != nil {
		return err
	}
	ws.Checkpoints.DeleteSession(sessionID)
	return nil
}

// ListUserMessages returns user-role messages for a session.
func (b *Backend) ListUserMessages(ctx context.Context, workspaceID, sessionID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Messages.ListUserMessages(ctx, sessionID)
}

// ListAllUserMessages returns all user-role messages across sessions.
func (b *Backend) ListAllUserMessages(ctx context.Context, workspaceID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Messages.ListAllUserMessages(ctx)
}

// ListCheckpoints returns the rewind checkpoints recorded for a
// session, oldest first.
func (b *Backend) ListCheckpoints(ctx context.Context, workspaceID, sessionID string) ([]checkpoints.Checkpoint, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Checkpoints.List(ctx, sessionID)
}

// Rewind restores a session to the state it was in just before the
// given user message was sent. It refuses while the session is busy:
// rewinding mid-run would race the tools still writing to the tree.
func (b *Backend) Rewind(ctx context.Context, workspaceID, sessionID, messageID string, mode checkpoints.Mode) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if ws.AgentCoordinator != nil && ws.AgentCoordinator.IsSessionBusy(sessionID) {
		return fmt.Errorf("cannot rewind: %w", ErrSessionBusy)
	}
	mode, err = normalizeRewindMode(mode)
	if err != nil {
		return err
	}
	return ws.Checkpoints.Rewind(ctx, sessionID, messageID, mode)
}

func normalizeRewindMode(mode checkpoints.Mode) (checkpoints.Mode, error) {
	if mode == "" {
		return checkpoints.ModeBoth, nil
	}
	m, err := checkpoints.ParseMode(string(mode))
	if err != nil {
		return mode, fmt.Errorf("%w: rewind mode %q", ErrInvalidArgument, string(mode))
	}
	return m, nil
}
