package history

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/pubsub"
)

const (
	InitialVersion = 0
)

type File struct {
	ID        string
	SessionID string
	Path      string
	Content   string
	Version   int64
	CreatedAt int64
	UpdatedAt int64
}

// Service manages file versions and history for sessions.
type Service interface {
	pubsub.Subscriber[File]
	Create(ctx context.Context, sessionID, path, content string) (File, error)

	// CreateVersion creates a new version of a file.
	CreateVersion(ctx context.Context, sessionID, path, content string) (File, error)

	Get(ctx context.Context, id string) (File, error)
	GetByPathAndSession(ctx context.Context, path, sessionID string) (File, error)
	// ListBySessionWithChildren returns the file history for sessionID plus
	// its direct child (subagent) sessions in a single query.
	ListBySessionWithChildren(ctx context.Context, sessionID string) ([]File, error)
}

type service struct {
	*pubsub.Broker[File]
	q *db.Queries
}

func NewService(q *db.Queries) Service {
	return &service{
		Broker: pubsub.NewBroker[File](),
		q:      q,
	}
}

func (s *service) Create(ctx context.Context, sessionID, path, content string) (File, error) {
	return s.createWithVersion(ctx, sessionID, path, content, InitialVersion)
}

// CreateVersion creates a new version of a file with auto-incremented version
// number. If no previous versions exist for the path, it creates the initial
// version. The provided content is stored as the new version.
func (s *service) CreateVersion(ctx context.Context, sessionID, path, content string) (File, error) {
	latest, err := s.q.GetLatestFileVersion(ctx, path)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return s.Create(ctx, sessionID, path, content)
	case err != nil:
		return File{}, err
	}
	return s.createWithVersion(ctx, sessionID, path, content, latest+1)
}

// createWithVersion inserts the file at version, stepping the version up
// when a concurrent writer already took it.
func (s *service) createWithVersion(ctx context.Context, sessionID, path, content string, version int64) (File, error) {
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		dbFile, err := s.q.CreateFile(ctx, db.CreateFileParams{
			ID:        uuid.New().String(),
			SessionID: sessionID,
			Path:      path,
			Content:   content,
			Version:   version,
		})
		if err != nil {
			if attempt < maxAttempts && strings.Contains(err.Error(), "UNIQUE constraint failed") {
				version++
				continue
			}
			return File{}, err
		}
		file := s.fromDBItem(dbFile)
		s.Publish(pubsub.CreatedEvent, file)
		return file, nil
	}
}

func (s *service) Get(ctx context.Context, id string) (File, error) {
	dbFile, err := s.q.GetFile(ctx, id)
	if err != nil {
		return File{}, err
	}
	return s.fromDBItem(dbFile), nil
}

func (s *service) GetByPathAndSession(ctx context.Context, path, sessionID string) (File, error) {
	dbFile, err := s.q.GetFileByPathAndSession(ctx, db.GetFileByPathAndSessionParams{
		Path:      path,
		SessionID: sessionID,
	})
	if err != nil {
		return File{}, err
	}
	return s.fromDBItem(dbFile), nil
}

// ListBySessionWithChildren returns the file history for sessionID plus the
// file history of its direct child (subagent) sessions, in a single query
// instead of one round trip per child.
func (s *service) ListBySessionWithChildren(ctx context.Context, sessionID string) ([]File, error) {
	dbFiles, err := s.q.ListFilesBySessionWithChildren(ctx, db.ListFilesBySessionWithChildrenParams{
		SessionID:       sessionID,
		ParentSessionID: sql.NullString{String: sessionID, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	files := make([]File, len(dbFiles))
	for i, dbFile := range dbFiles {
		files[i] = s.fromDBItem(dbFile)
	}
	return files, nil
}

func (s *service) fromDBItem(item db.File) File {
	return File{
		ID:        item.ID,
		SessionID: item.SessionID,
		Path:      item.Path,
		Content:   item.Content,
		Version:   item.Version,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}
