// Package filetracker provides functionality to track file reads in sessions.
package filetracker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/stubbedev/harness/internal/db"
)

// Service defines the interface for tracking file reads in sessions.
type Service interface {
	// RecordRead records when a file was read.
	RecordRead(ctx context.Context, sessionID, path string)

	// LastReadTime returns when a file was last read.
	// Returns zero time if never read.
	LastReadTime(ctx context.Context, sessionID, path string) time.Time

	// ListReadFiles returns the paths of all files read in a session.
	ListReadFiles(ctx context.Context, sessionID string) ([]string, error)
}

type service struct {
	q        *db.Queries
	evidence evidenceStore
	// baseDir is what stored paths are relative to; empty means the
	// process working directory.
	baseDir string
}

// ServiceOption configures a file tracker service.
type ServiceOption func(*service)

// WithBaseDir stores read paths relative to dir, the workspace, instead of
// the process working directory. A server hosts workspaces the process was
// not started in, and a path relative to its own directory would not
// survive it being started from another.
func WithBaseDir(dir string) ServiceOption {
	return func(s *service) { s.baseDir = dir }
}

// NewService creates a new file tracker service.
func NewService(q *db.Queries, opts ...ServiceOption) Service {
	s := &service{q: q}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// base returns the directory stored paths are relative to.
func (s *service) base() (string, error) {
	if s.baseDir != "" {
		return s.baseDir, nil
	}
	return os.Getwd()
}

// RecordRead records when a file was read.
func (s *service) RecordRead(ctx context.Context, sessionID, path string) {
	if s.q == nil {
		return
	}
	if err := s.q.RecordFileRead(ctx, db.RecordFileReadParams{
		SessionID: sessionID,
		Path:      s.relpath(path),
	}); err != nil {
		slog.Error("Error recording file read", "error", err, "file", path)
	}
}

// LastReadTime returns when a file was last read.
// Returns zero time if never read.
func (s *service) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	readFile, err := s.q.GetFileRead(ctx, db.GetFileReadParams{
		SessionID: sessionID,
		Path:      s.relpath(path),
	})
	if err != nil {
		return time.Time{}
	}

	return time.Unix(readFile.ReadAt, 0)
}

func (s *service) relpath(path string) string {
	path = filepath.Clean(path)
	basepath, err := s.base()
	if err != nil {
		slog.Warn("Error getting basepath", "error", err)
		return path
	}
	relpath, err := filepath.Rel(basepath, path)
	if err != nil {
		slog.Warn("Error getting relpath", "error", err)
		return path
	}
	return relpath
}

// ListReadFiles returns the paths of all files read in a session.
func (s *service) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	readFiles, err := s.q.ListSessionReadFiles(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing read files: %w", err)
	}

	basepath, err := s.base()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	paths := make([]string, 0, len(readFiles))
	for _, rf := range readFiles {
		paths = append(paths, filepath.Join(basepath, rf.Path))
	}
	return paths, nil
}
