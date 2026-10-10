package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

// pragmas are applied to every new connection. secure_delete is FAST
// rather than ON: freed pages are still zeroed where that costs no
// extra I/O, but deletes no longer scrub every freed page. Nothing in
// the repo relies on full page zeroing, so FAST keeps most of the
// benefit without the per-delete cost.
var (
	pragmas = map[string]string{
		"foreign_keys":  "ON",
		"journal_mode":  "WAL",
		"page_size":     "4096",
		"temp_store":    "MEMORY",
		"cache_size":    "-8000",
		"synchronous":   "NORMAL",
		"secure_delete": "FAST",
		"busy_timeout":  "30000",
	}
	// readerPragmas are applied to every reader connection. query_only
	// makes a write that reached a reader by mistake fail instead of
	// becoming a second writer; the file-level settings (journal mode,
	// page size) are the writer's to make.
	readerPragmas = map[string]string{
		"query_only":   "1",
		"temp_store":   "MEMORY",
		"cache_size":   "-8000",
		"busy_timeout": "30000",
	}
	gooseInitOnce sync.Once
	gooseInitErr  error
)

const (
	// readerConns bounds the reader pool. Reads come from the UI, the
	// agent loop and its tools; a handful covers their overlap.
	readerConns = 4
	// readerIdleTime closes reader connections nothing has used for a
	// while, along with the page cache each one holds.
	readerIdleTime = 5 * time.Minute
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
	// reader is the read-only pool opened alongside db, or nil when it
	// could not be opened; see [Routed].
	reader *sql.DB
	// unlock releases the data-dir lock, or is nil when none is held.
	unlock func()
}

// close closes the connection and drops the data-dir lock, if held. The
// readers close first, so the writer is the last connection to the file
// and the one that checkpoints the WAL on its way out.
func (e *connEntry) close() error {
	var err error
	if e.reader != nil {
		err = e.reader.Close()
	}
	err = errors.Join(err, e.db.Close())
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
	lockDir := ""
	if wantLock {
		lockDir = dataDir
	}
	return connect(ctx, filepath.Join(dataDir, "harness.db"), lockDir, migrateWorkspace)
}

// ConnectFile opens the SQLite database at dbPath with the driver
// configuration [Connect] uses and applies the goose migrations at the
// root of migrations, which are tracked in that database alone: the set
// is independent of the workspace schema. Connections are pooled by path
// as with Connect; pair each call with a [ReleaseFile]. It is for stores
// that are not a workspace database, such as the machine-wide memory
// store several harness processes share.
func ConnectFile(ctx context.Context, dbPath string, migrations fs.FS) (*sql.DB, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path is empty")
	}
	return connect(ctx, dbPath, "", func(ctx context.Context, conn *sql.DB) error {
		provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrations,
			goose.WithDisableGlobalRegistry(true),
		)
		if err != nil {
			return err
		}
		_, err = provider.Up(ctx)
		return err
	})
}

// ReleaseFile is [Release] for a database opened with [ConnectFile].
func ReleaseFile(dbPath string) error {
	return release(poolKey(dbPath))
}

// migrateWorkspace applies the workspace schema embedded in [FS].
func migrateWorkspace(_ context.Context, conn *sql.DB) error {
	if err := initGoose(); err != nil {
		slog.Error("Failed to initialize goose", "error", err)
		return fmt.Errorf("failed to initialize goose: %w", err)
	}
	return goose.Up(conn, "migrations")
}

// connect opens (or shares) the pooled connection to dbPath and migrates
// it. A non-empty lockDir is the data directory whose lock the
// connection must hold.
func connect(ctx context.Context, dbPath, lockDir string, migrate func(context.Context, *sql.DB) error) (*sql.DB, error) {
	absPath := poolKey(dbPath)

	poolMu.Lock()
	defer poolMu.Unlock()

	if entry, ok := pool[absPath]; ok {
		if lockDir != "" && entry.unlock == nil {
			unlock, err := acquireDataDirLock(lockDir)
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
	// harness process on the same SQLite file. Ensuring the directory
	// exists is required because the lock file lives inside it. Locking
	// is opt-in via WithDataDirLock so that local-mode invocations do
	// not refuse a second harness against the same data dir until
	// client/server becomes the default.
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create data directory %q: %w", dir, err)
	}
	entry := &connEntry{refCount: 1}
	if lockDir != "" {
		unlock, err := acquireDataDirLock(lockDir)
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
	// resulting in SQLITE_NOTADB (26) on the next open. Reads have a
	// query_only pool of their own, opened below.
	conn.SetMaxOpenConns(1)

	if err = conn.PingContext(ctx); err != nil {
		entry.close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := migrate(ctx, conn); err != nil {
		entry.close()
		slog.Error("Failed to apply migrations", "path", dbPath, "error", err)
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	// Reads get a pool of their own. In WAL mode readers never wait on
	// the writer, but behind the single writer connection every read
	// queued behind every write, a burst of parallel tool results
	// included. The pool connects lazily, so a caller that never routes
	// a read through it never holds the file open twice.
	if reader, readerErr := openReader(dbPath); readerErr != nil {
		slog.Warn("Failed to open read-only database pool; reads share the writer", "error", readerErr)
	} else {
		reader.SetMaxOpenConns(readerConns)
		reader.SetMaxIdleConns(readerConns)
		reader.SetConnMaxIdleTime(readerIdleTime)
		entry.reader = reader
	}

	runtime.GC()
	debug.FreeOSMemory()

	pool[absPath] = entry
	return conn, nil
}

// Routed returns the DBTX for conn that [New] should be given: read-only
// statements go to the reader pool Connect opened alongside conn, and
// everything else to conn itself. A conn without a reader pool, or one
// Connect did not open, is returned as is. Transactions are unaffected:
// they are begun on conn and bound with [Queries.WithTx].
func Routed(conn *sql.DB) DBTX {
	poolMu.Lock()
	defer poolMu.Unlock()
	for _, entry := range pool {
		if entry.db == conn && entry.reader != nil {
			return routedDB{writer: conn, reader: entry.reader}
		}
	}
	return conn
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
	return release(poolKey(filepath.Join(dataDir, "harness.db")))
}

// release drops one reference to the pooled entry at absPath, closing
// it with the last.
func release(absPath string) error {
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
