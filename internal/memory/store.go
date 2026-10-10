package memory

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/lock"
	"github.com/stubbedev/harness/internal/memory/memdb"
)

const (
	// storeFile is the store's database inside its directory.
	storeFile = "memory.db"
	// storeLockFile serializes opening the store across processes: the
	// migrations and the legacy import run under it.
	storeLockFile = "memory.lock"
	// storeLockTimeout bounds the wait for another process's open. Past
	// it the open goes ahead anyway: migrations are idempotent, and the
	// import records each workspace in the same transaction that copies
	// it.
	storeLockTimeout = 30 * time.Second
)

// Store is the machine-wide memory database. Every workspace in every
// harness process on the machine opens the same file, so one database
// carries the global memories and those of every repository. Open it
// with [OpenStore]; a process that opens it several times (one per
// workspace in server mode) shares one pooled connection.
type Store struct {
	path string
	// conn is the single writer connection; transactions begin on it.
	conn *sql.DB
	// writer runs statements on conn, queries routes reads to the
	// reader pool, and reader is the routed handle the hand-written
	// full-text query runs on.
	writer  *memdb.Queries
	queries *memdb.Queries
	reader  memdb.DBTX
}

// OpenStore opens (creating when needed) the store in dir, applying its
// migrations. Pair it with [Store.Close].
func OpenStore(ctx context.Context, dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating memory store directory: %w", err)
	}
	release := lockStore(ctx, dir)
	defer release()

	path := StorePath(dir)
	conn, err := db.ConnectFile(ctx, path, memdb.Migrations())
	if err != nil {
		return nil, fmt.Errorf("opening memory store: %w", err)
	}
	routed := db.Routed(conn)
	return &Store{
		path:    path,
		conn:    conn,
		writer:  memdb.New(conn),
		queries: memdb.New(routed),
		reader:  routed,
	}, nil
}

// StorePath returns the database file of the store kept in dir.
func StorePath(dir string) string {
	return filepath.Join(dir, storeFile)
}

// lockStore takes the cross-process lock that serializes opening the
// store, returning its release. A lock that cannot be had (a platform
// without file locks, another process stuck past the timeout) is logged
// and skipped rather than failing the open.
func lockStore(ctx context.Context, dir string) func() {
	ctx, cancel := context.WithTimeout(ctx, storeLockTimeout)
	defer cancel()
	release, err := lock.File(ctx, filepath.Join(dir, storeLockFile))
	if err != nil {
		slog.Warn("Opening the memory store without its lock", "error", err)
		return func() {}
	}
	return release
}

// Close releases this open of the store.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return db.ReleaseFile(s.path)
}
