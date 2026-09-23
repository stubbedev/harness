package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"github.com/pressly/goose/v3"
)

var (
	pragmas = map[string]string{
		"foreign_keys":  "ON",
		"journal_mode":  "WAL",
		"page_size":     "4096",
		"temp_store":    "MEMORY",
		"cache_size":    "-8000",
		"synchronous":   "NORMAL",
		"secure_delete": "ON",
		"busy_timeout":  "30000",
	}
	gooseInitOnce sync.Once
	gooseInitErr  error
)

//go:embed migrations/*.sql
var FS embed.FS

func init() {
	goose.SetBaseFS(FS)

	if testing.Testing() {
		goose.SetLogger(goose.NopLogger())
	}
}

// connEntry holds a shared database connection, its reference count,
// and the release for the data-directory lock that gates access to
// this entry. The lock is acquired at most once per entry (when the
// first Connect that asks for it arrives) and released when the last
// reference is dropped, which lets the same process open the same data
// directory concurrently while still blocking a second harness process
// from racing the storage.
type connEntry struct {
	db       *sql.DB
	refCount int
	// unlock releases the data-dir lock, or is nil when none is held.
	unlock func()
}

// close closes the connection and drops the data-dir lock, if held.
func (e *connEntry) close() error {
	err := e.db.Close()
	if e.unlock != nil {
		e.unlock()
	}
	return err
}

var (
	pool   = make(map[string]*connEntry)
	poolMu sync.Mutex
)

// ConnectOption configures a Connect call. Options are applied in
// order; later options override earlier ones for the same field.
type ConnectOption func(*connectOptions)

// connectOptions holds the resolved configuration for a Connect call.
type connectOptions struct {
	lockDataDir bool
}

// WithDataDirLock toggles acquisition of the per-data-directory lock
// for this Connect call. The lock is off by default so local-mode
// invocations do not regress today's behavior; the server's
// workspace-bootstrap path opts in. HARNESS_SKIP_DATADIR_LOCK still
// bypasses acquisition even when this option is set.
func WithDataDirLock(enable bool) ConnectOption {
	return func(o *connectOptions) { o.lockDataDir = enable }
}

// Connect opens a SQLite database connection for the given data
// directory and runs migrations. If a connection to the same database
// file already exists, the existing connection is returned with its
// reference count incremented. Callers must pair each Connect with a
// [Release] when they no longer need the connection.
//
// A Connect that asks for the data-dir lock always ends up holding it,
// even when an earlier unlocked Connect already opened the entry.
func Connect(ctx context.Context, dataDir string, opts ...ConnectOption) (*sql.DB, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data.dir is not set")
	}

	var cfg connectOptions
	for _, opt := range opts {
		opt(&cfg)
	}
	wantLock := cfg.lockDataDir && !skipDataDirLock()

	dbPath := filepath.Join(dataDir, "harness.db")
	absPath := poolKey(dbPath)

	poolMu.Lock()
	defer poolMu.Unlock()

	if entry, ok := pool[absPath]; ok {
		if wantLock && entry.unlock == nil {
			unlock, err := acquireDataDirLock(dataDir)
			if err != nil {
				return nil, err
			}
			entry.unlock = unlock
		}
		entry.refCount++
		return entry.db, nil
	}

	// Take the per-data-directory lock before opening the database so
	// we fail fast and with a clear error rather than racing another
	// harness process on the same SQLite file. Ensuring the data
	// directory exists is required because the lock file lives inside
	// it. Locking is opt-in via WithDataDirLock so that local-mode
	// invocations do not refuse a second harness against the same data
	// dir until client/server becomes the default.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create data directory %q: %w", dataDir, err)
	}
	entry := &connEntry{refCount: 1}
	if wantLock {
		unlock, err := acquireDataDirLock(dataDir)
		if err != nil {
			return nil, err
		}
		entry.unlock = unlock
	}

	conn, err := openDB(dbPath)
	if err != nil {
		if entry.unlock != nil {
			entry.unlock()
		}
		return nil, err
	}
	entry.db = conn

	// Serialize all access through a single connection. SQLite
	// serializes writes at the file level anyway, and allowing multiple
	// pool connections to interleave writes/checkpoints (especially
	// under concurrent sub-agents) has caused WAL/header desync
	// resulting in SQLITE_NOTADB (26) on the next open.
	conn.SetMaxOpenConns(1)

	if err = conn.PingContext(ctx); err != nil {
		entry.close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := initGoose(); err != nil {
		entry.close()
		slog.Error("Failed to initialize goose", "error", err)
		return nil, fmt.Errorf("failed to initialize goose: %w", err)
	}

	if err := goose.Up(conn, "migrations"); err != nil {
		entry.close()
		slog.Error("Failed to apply migrations", "error", err)
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	runtime.GC()
	debug.FreeOSMemory()

	pool[absPath] = entry
	return conn, nil
}

// poolKey resolves dbPath to an absolute path so that different
// relative paths to the same file share a single connection.
func poolKey(dbPath string) string {
	if abs, err := filepath.Abs(dbPath); err == nil {
		return abs
	}
	return dbPath
}

// uriPath escapes the characters that would end or corrupt the path
// part of a SQLite "file:" URI, so a data directory containing '?',
// '#' or '%' opens the file it names.
func uriPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

// Release decrements the reference count for the database at the given
// data directory. When the count reaches zero the underlying connection
// is closed and removed from the pool.
func Release(dataDir string) error {
	absPath := poolKey(filepath.Join(dataDir, "harness.db"))

	poolMu.Lock()
	defer poolMu.Unlock()

	entry, ok := pool[absPath]
	if !ok {
		return nil
	}

	entry.refCount--
	if entry.refCount > 0 {
		return nil
	}

	delete(pool, absPath)
	return entry.close()
}

// ResetPool closes all pooled connections and clears the pool. This is
// intended for use in tests to ensure a clean state between test cases.
func ResetPool() {
	poolMu.Lock()
	defer poolMu.Unlock()
	for path, entry := range pool {
		entry.close()
		delete(pool, path)
	}
}

// ConnectReadOnly opens a read-only SQLite database connection without running
// migrations. Used for aggregating stats across multiple project databases.
func ConnectReadOnly(ctx context.Context, dbPath string) (*sql.DB, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path is empty")
	}

	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return db, nil
}

func initGoose() error {
	gooseInitOnce.Do(func() {
		goose.SetBaseFS(FS)
		gooseInitErr = goose.SetDialect("sqlite3")
	})

	return gooseInitErr
}
