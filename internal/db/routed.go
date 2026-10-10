package db

import (
	"context"
	"database/sql"
	"strings"
)

// routedDB is the DBTX [Routed] hands out: statements that only read go
// to the reader pool, everything else to the single writer connection.
// The split is by statement rather than by method, because sqlc runs
// INSERT ... RETURNING through QueryRowContext just as it runs a SELECT.
type routedDB struct {
	writer *sql.DB
	reader *sql.DB
}

func (r routedDB) pick(query string) *sql.DB {
	if readOnlyStatement(query) {
		return r.reader
	}
	return r.writer
}

func (r routedDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return r.writer.ExecContext(ctx, query, args...)
}

func (r routedDB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return r.pick(query).PrepareContext(ctx, query)
}

func (r routedDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.pick(query).QueryContext(ctx, query, args...)
}

func (r routedDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return r.pick(query).QueryRowContext(ctx, query, args...)
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
