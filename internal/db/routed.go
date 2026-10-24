package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// routedDB is the DBTX [Routed] hands out: statements that only read go
// to the reader pool, everything else to the single writer connection.
// The split is by statement rather than by method, because sqlc runs
// INSERT ... RETURNING through QueryRowContext just as it runs a SELECT.
//
// Every query runs through a statement prepared once and kept in stmts,
// so SQLite parses its SQL a single time instead of once per call.
type routedDB struct {
	writer *sql.DB
	reader *sql.DB
	stmts  *stmtCache
}

func (r routedDB) pick(query string) *sql.DB {
	if readOnlyStatement(query) {
		return r.reader
	}
	return r.writer
}

func (r routedDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	pool := r.pick(query)
	if result, ok := r.stmts.exec(ctx, pool, query, args...); ok {
		return result, nil
	}
	return pool.ExecContext(ctx, query, args...)
}

func (r routedDB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return r.pick(query).PrepareContext(ctx, query)
}

func (r routedDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	pool := r.pick(query)
	if rows, ok := r.stmts.query(ctx, pool, query, args...); ok {
		return rows, nil
	}
	return pool.QueryContext(ctx, query, args...)
}

func (r routedDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	pool := r.pick(query)
	if row, ok := r.stmts.queryRow(ctx, pool, query, args...); ok {
		return row
	}
	return pool.QueryRowContext(ctx, query, args...)
}

// stmtKey identifies a cached statement: the pool it was prepared on and
// the SQL text. Writers and readers are different pools, and the same
// text prepared on each is a different statement.
type stmtKey struct {
	pool  *sql.DB
	query string
}

// maxCachedStatements bounds the cache. Every statement the app runs is
// a fixed sqlc query, so the cap is far above the set that is ever
// reached; it only keeps a hand-built query with varying text from
// growing the map without end.
const maxCachedStatements = 256

// stmtCache holds one prepared statement per (pool, SQL text), prepared
// on first use. Prepared statements belong to the pool that prepared
// them, so each is closed when that pool is: see connEntry.close.
type stmtCache struct {
	mu     sync.Mutex
	stmts  map[stmtKey]*sql.Stmt
	closed bool
}

func newStmtCache() *stmtCache {
	return &stmtCache{stmts: make(map[stmtKey]*sql.Stmt, maxCachedStatements)}
}

// exec, query and queryRow run the statement for query on pool, prepared
// once and reused. Each reports false when the cache has no statement to
// run, leaving the caller to run the query directly: the pool then
// produces the same error, and a statement the cache declined to keep
// still works by not being kept.
func (c *stmtCache) exec(ctx context.Context, pool *sql.DB, query string, args ...any) (sql.Result, bool) {
	if c == nil {
		return nil, false
	}
	//nolint:sqlclosecheck // the cache owns the statement; connEntry.close closes it
	stmt, err := c.get(ctx, pool, query)
	if err != nil {
		return nil, false
	}
	result, err := stmt.ExecContext(ctx, args...)
	return result, err == nil
}

func (c *stmtCache) query(ctx context.Context, pool *sql.DB, query string, args ...any) (*sql.Rows, bool) {
	if c == nil {
		return nil, false
	}
	//nolint:sqlclosecheck // the cache owns the statement; connEntry.close closes it
	stmt, err := c.get(ctx, pool, query)
	if err != nil {
		return nil, false
	}
	rows, err := stmt.QueryContext(ctx, args...)
	return rows, err == nil
}

func (c *stmtCache) queryRow(ctx context.Context, pool *sql.DB, query string, args ...any) (*sql.Row, bool) {
	if c == nil {
		return nil, false
	}
	//nolint:sqlclosecheck // the cache owns the statement; connEntry.close closes it
	stmt, err := c.get(ctx, pool, query)
	if err != nil {
		return nil, false
	}
	return stmt.QueryRowContext(ctx, args...), true
}

// get returns the statement for query on pool, preparing and caching it
// on first use.
func (c *stmtCache) get(ctx context.Context, pool *sql.DB, query string) (*sql.Stmt, error) {
	key := stmtKey{pool: pool, query: query}

	c.mu.Lock()
	stmt, ok := c.stmts[key]
	closed := c.closed
	c.mu.Unlock()
	if ok {
		return stmt, nil
	}
	if closed {
		return nil, errStmtCacheClosed
	}

	prepared, err := pool.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return c.adopt(key, prepared)
}

// adopt stores prepared under key and returns the statement the caller
// should use. A statement another caller stored first wins, and a cache
// that refuses the statement closes it and reports why.
func (c *stmtCache) adopt(key stmtKey, prepared *sql.Stmt) (*sql.Stmt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Another caller may have prepared the same statement while this
	// one was preparing; the cache keeps exactly one.
	if existing, ok := c.stmts[key]; ok {
		defer prepared.Close()
		return existing, nil
	}
	if c.closed || len(c.stmts) >= maxCachedStatements {
		defer prepared.Close()
		return nil, errStmtNotCached
	}
	c.stmts[key] = prepared
	return prepared, nil
}

// close closes every cached statement and refuses new ones. The pool
// closes them too, but closing first keeps the order explicit: no
// statement outlives the connection it was prepared on.
func (c *stmtCache) close() error {
	c.mu.Lock()
	c.closed = true
	stmts := c.stmts
	c.stmts = nil
	c.mu.Unlock()

	var err error
	for key, stmt := range stmts {
		if closeErr := stmt.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close cached statement %q: %w", firstLine(key.query), closeErr))
		}
	}
	return err
}

// errStmtCacheClosed and errStmtNotCached report that a statement was
// not cached. Both leave the caller to run its query directly.
var (
	errStmtCacheClosed = errors.New("statement cache closed")
	errStmtNotCached   = errors.New("statement not cached")
)

// firstLine returns query's first non-empty line, naming a statement in
// a log line without spilling a multi-line query into it.
func firstLine(query string) string {
	for line := range strings.SplitSeq(query, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// readOnlyStatement reports whether query is a plain SELECT once the
// comments sqlc leads every query with are skipped. Anything else - a
// write, a CTE, a PRAGMA - is treated as a write, so a statement this
// misjudges lands on the writer, where it works, never on a reader,
// where query_only would refuse it.
func readOnlyStatement(query string) bool {
	for {
		query = strings.TrimLeft(query, " \t\r\n")
		switch {
		case strings.HasPrefix(query, "--"):
			end := strings.IndexByte(query, '\n')
			if end < 0 {
				return false
			}
			query = query[end+1:]
		case strings.HasPrefix(query, "/*"):
			end := strings.Index(query, "*/")
			if end < 0 {
				return false
			}
			query = query[end+2:]
		default:
			const keyword = "SELECT"
			if len(query) < len(keyword) || !strings.EqualFold(query[:len(keyword)], keyword) {
				return false
			}
			if len(query) == len(keyword) {
				return true
			}
			return !isIdentByte(query[len(keyword)])
		}
	}
}

// isIdentByte reports whether b can continue an SQL identifier, so
// SELECTED is not read as SELECT.
func isIdentByte(b byte) bool {
	return b == '_' || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z') || ('0' <= b && b <= '9')
}
