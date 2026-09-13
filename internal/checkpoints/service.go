// Package checkpoints implements per-turn workspace snapshots on top
// of a shadow git repository, so a session can be rewound to an
// earlier turn: the conversation, the files on disk, or both.
//
// The shadow repository is a private GIT_DIR stored inside the
// workspace data directory, pointed at the user's working tree. It
// never touches the user's own git history, index, or refs, and it
// honors the project's .gitignore files, so ignored files (build
// output, node_modules) are neither snapshotted nor removed by a
// restore. Files untracked at snapshot time but not ignored are
// captured, because a restore that skips them would not be a restore.
package checkpoints

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"

	"github.com/google/uuid"
	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

// Mode selects what a rewind restores.
type Mode string

const (
	// ModeConversation rewinds the transcript: the selected user
	// message and everything after it is deleted, so the session
	// continues as if the turn had never been sent.
	ModeConversation Mode = "conversation"
	// ModeFiles rewinds the working tree to the snapshot taken when
	// the selected prompt was submitted.
	ModeFiles Mode = "files"
	// ModeBoth rewinds the transcript and the working tree.
	ModeBoth Mode = "both"
)

// ParseMode converts a wire string into a Mode.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeConversation, ModeFiles, ModeBoth:
		return Mode(s), nil
	default:
		return "", fmt.Errorf("invalid rewind mode %q", s)
	}
}

// Checkpoint maps a user message to the git commit that snapshots the
// working tree at the moment the prompt was submitted.
type Checkpoint struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
	CommitSHA string `json:"commit_sha"`
	CreatedAt int64  `json:"created_at"`
}

// Service snapshots and restores the working tree, records the
// snapshot refs in the database, and rewinds sessions. A nil Service
// (or one constructed where git is unavailable) disables checkpoints:
// Snapshot becomes a no-op and List stays empty.
type Service struct {
	q          db.Querier
	workingDir string
	dataDir    string
	messages   message.Service
	sessions   session.Service
	enabled    bool
}

// NewService builds a checkpoints service. The messages and sessions
// services are used to truncate the transcript on rewind. When git
// is not executable the service disables itself: snapshots become
// no-ops and rewinds stay available in conversation-only form.
func NewService(q db.Querier, workingDir, dataDir string, messages message.Service, sessions session.Service) *Service {
	s := &Service{
		q:          q,
		workingDir: workingDir,
		dataDir:    dataDir,
		messages:   messages,
		sessions:   sessions,
		enabled:    true,
	}
	if _, err := exec.LookPath("git"); err != nil {
		slog.Info("git not found; file checkpoints are disabled")
		s.enabled = false
	}
	return s
}

// Enabled reports whether snapshots can be taken.
func (s *Service) Enabled() bool {
	return s != nil && s.enabled
}

// Snapshot records the working tree for sessionID under messageID. It
// is called as a user turn starts, before any tool has run, so the
// snapshot is the state the prompt was submitted into. Errors are
// returned to the caller, which treats a failed snapshot as
// non-fatal: the turn proceeds, only without a rewind point.
func (s *Service) Snapshot(ctx context.Context, sessionID, messageID string) error {
	if s == nil || !s.enabled {
		return nil
	}
	sha, err := s.commitTree(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	_, err = s.q.CreateCheckpoint(ctx, db.CreateCheckpointParams{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		MessageID: messageID,
		CommitSha: sha,
	})
	if err != nil {
		return fmt.Errorf("recording checkpoint: %w", err)
	}
	return nil
}

// List returns every checkpoint recorded for a session, oldest first.
func (s *Service) List(ctx context.Context, sessionID string) ([]Checkpoint, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.q.ListCheckpointsBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Checkpoint, len(rows))
	for i, row := range rows {
		out[i] = Checkpoint{
			ID:        row.ID,
			SessionID: row.SessionID,
			MessageID: row.MessageID,
			CommitSHA: row.CommitSha,
			CreatedAt: row.CreatedAt,
		}
	}
	return out, nil
}

// Get returns the checkpoint recorded for a single message.
func (s *Service) Get(ctx context.Context, messageID string) (Checkpoint, error) {
	if s == nil {
		return Checkpoint{}, fmt.Errorf("checkpoints are unavailable")
	}
	row, err := s.q.GetCheckpointByMessage(ctx, messageID)
	if err != nil {
		return Checkpoint{}, err
	}
	return Checkpoint{
		ID:        row.ID,
		SessionID: row.SessionID,
		MessageID: row.MessageID,
		CommitSHA: row.CommitSha,
		CreatedAt: row.CreatedAt,
	}, nil
}

// Rewind restores the session to the state it was in just before the
// given user message was sent. Conversation mode deletes the message
// and everything after it; files mode restores the snapshot committed
// at submit time. Rewinding while the session is busy is the caller's
// responsibility to prevent.
func (s *Service) Rewind(ctx context.Context, sessionID, messageID string, mode Mode) error {
	if s == nil {
		return fmt.Errorf("checkpoints are unavailable")
	}
	if mode != ModeConversation {
		cp, err := s.Get(ctx, messageID)
		if err != nil {
			return fmt.Errorf("no snapshot for this turn, try conversation-only rewind: %w", err)
		}
		if err := s.restore(ctx, sessionID, cp.CommitSHA); err != nil {
			return fmt.Errorf("restoring files: %w", err)
		}
	}
	if mode == ModeFiles {
		return nil
	}
	return s.truncateConversation(ctx, sessionID, messageID)
}

// truncateConversation deletes messageID and every message after it,
// newest first so subscribers observe an unwinding transcript, and
// clears a summary pointer that would otherwise dangle.
func (s *Service) truncateConversation(ctx context.Context, sessionID, messageID string) error {
	msgs, err := s.messages.List(ctx, sessionID)
	if err != nil {
		return err
	}
	idx := slices.IndexFunc(msgs, func(m message.Message) bool {
		return m.ID == messageID
	})
	if idx < 0 {
		return fmt.Errorf("message %s not found in session %s", messageID, sessionID)
	}
	doomed := msgs[idx:]
	for _, m := range slices.Backward(doomed) {
		if err := s.messages.Delete(ctx, m.ID); err != nil {
			return fmt.Errorf("deleting message %s: %w", m.ID, err)
		}
	}

	sess, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil
	}
	if sess.SummaryMessageID == "" {
		return nil
	}
	if slices.ContainsFunc(doomed, func(m message.Message) bool {
		return m.ID == sess.SummaryMessageID
	}) {
		sess.SummaryMessageID = ""
		if _, err := s.sessions.Save(ctx, sess); err != nil {
			slog.Warn("Failed to clear summary pointer after rewind", "error", err)
		}
	}
	return nil
}

// DeleteSession removes the snapshot objects a session accumulated.
// Checkpoint rows are removed by the sessions foreign key; this only
// reclaims the shadow repository on disk.
func (s *Service) DeleteSession(sessionID string) {
	if s == nil {
		return
	}
	if err := removeShadow(s.dataDir, sessionID); err != nil {
		slog.Warn("Failed to remove checkpoint objects for session", "sessionID", sessionID, "error", err)
	}
}
