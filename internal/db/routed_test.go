package db

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadOnlyStatement(t *testing.T) {
	t.Parallel()

	for query, want := range map[string]bool{
		"-- name: GetSessionByID :one\nSELECT * FROM sessions WHERE id = ?": true,
		"select 1":               true,
		"  /* lead */ SELECT\n1": true,
		"SELECT":                 true,
		"-- name: CreateSession :one\nINSERT INTO sessions (id) VALUES (?) RETURNING *": false,
		"UPDATE sessions SET title = ?":                 false,
		"DELETE FROM messages WHERE id = ? RETURNING *": false,
		"WITH x AS (SELECT 1) DELETE FROM messages":     false,
		"PRAGMA journal_mode":                           false,
		"SELECTED":                                      false,
		"-- only a comment":                             false,
		"/* unterminated":                               false,
		"":                                              false,
	} {
		require.Equal(t, want, readOnlyStatement(query), query)
	}
}

func newRoutedQueries(t *testing.T) (*sql.DB, *Queries, string) {
	t.Helper()
	dataDir := t.TempDir()
	conn, err := Connect(t.Context(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = Release(dataDir) })
	_, ok := Routed(conn).(routedDB)
	require.True(t, ok, "Connect opened no reader pool")
	return conn, New(Routed(conn)), dataDir
}

// A read must not queue behind the writer. A transaction holds the one
// writer connection for as long as it is open; a read sent to that
// connection waits it out, a routed one does not.
func TestRoutedReadsDoNotWaitForTheWriter(t *testing.T) {
	t.Parallel()

	conn, q, _ := newRoutedQueries(t)
	created, err := q.CreateSession(t.Context(), CreateSessionParams{ID: "s1", Title: "one"})
	require.NoError(t, err)

	tx, err := conn.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	err = q.WithTx(tx).RenameSession(t.Context(), RenameSessionParams{ID: "s1", Title: "renamed"})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got, err := q.GetSessionByID(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "one", got.Title, "a reader sees committed state only")

	blocked, cancelBlocked := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelBlocked()
	_, err = New(conn).GetSessionByID(blocked, created.ID)
	require.ErrorIs(t, err, context.DeadlineExceeded, "a read on the writer should have waited for the transaction")

	require.NoError(t, tx.Commit())
	got, err = q.GetSessionByID(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Title, "a reader sees a write as soon as it commits")
}

func TestRoutedReaderRefusesWrites(t *testing.T) {
	t.Parallel()

	conn, _, _ := newRoutedQueries(t)
	reader := Routed(conn).(routedDB).reader
	_, err := reader.ExecContext(t.Context(), "INSERT INTO sessions (id, title) VALUES ('x', 'x')")
	require.Error(t, err, "a reader connection must not write")

	// A RETURNING write runs through QueryRowContext and still lands on
	// the writer.
	q := New(Routed(conn))
	_, err = q.CreateSession(t.Context(), CreateSessionParams{ID: "s2", Title: "two"})
	require.NoError(t, err)
}

func TestReleaseClosesTheReaderPool(t *testing.T) {
	t.Parallel()

	conn, q, dataDir := newRoutedQueries(t)
	_, err := q.ListSessions(t.Context())
	require.NoError(t, err)
	reader := Routed(conn).(routedDB).reader

	require.NoError(t, Release(dataDir))
	require.Error(t, reader.PingContext(t.Context()), "reader pool outlived its writer")
	require.Equal(t, conn, Routed(conn), "a released connection routes nothing")
}

// cachedStatements is the number of statements a routed DBTX holds for a
// connection.
func cachedStatements(r routedDB) int {
	r.stmts.mu.Lock()
	defer r.stmts.mu.Unlock()
	return len(r.stmts.stmts)
}

// TestRoutedPreparesEachQueryOnce pins the cache: a query repeated at
// length keeps one prepared statement, and a read and a write are
// separate statements on their separate pools.
func TestRoutedPreparesEachQueryOnce(t *testing.T) {
	t.Parallel()

	conn, q, _ := newRoutedQueries(t)
	created, err := q.CreateSession(t.Context(), CreateSessionParams{ID: "s1", Title: "one"})
	require.NoError(t, err)
	routed := Routed(conn).(routedDB)

	round := func() {
		_, err := q.GetSessionByID(t.Context(), created.ID)
		require.NoError(t, err)
		require.NoError(t, q.RenameSession(t.Context(), RenameSessionParams{ID: created.ID, Title: "renamed"}))
	}
	round()
	first := cachedStatements(routed)
	require.Equal(t, 3, first, "one statement per distinct query: create, read, rename")

	for range 3 {
		round()
	}
	require.Equal(t, first, cachedStatements(routed), "repeats prepare nothing new")
}

// TestRoutedConcurrentFirstUsePreparesOnce pins the cache under the
// fan-out a tool burst creates: every caller preparing the same statement
// at once must still leave exactly one behind.
func TestRoutedConcurrentFirstUsePreparesOnce(t *testing.T) {
	t.Parallel()

	conn, _, _ := newRoutedQueries(t)
	routed := Routed(conn).(routedDB)

	const callers = 8
	ready := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-ready
			rows, err := routed.QueryContext(context.Background(), "SELECT 1")
			if err != nil {
				errs <- err
				return
			}
			defer rows.Close()
			errs <- rows.Err()
		})
	}
	close(ready)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, cachedStatements(routed))
}

// TestRoutedReportsDriverErrors pins the fallback: a statement that cannot
// be prepared still produces the driver's own error through every method,
// never a cache-specific one.
func TestRoutedReportsDriverErrors(t *testing.T) {
	t.Parallel()

	conn, _, _ := newRoutedQueries(t)
	routed := Routed(conn).(routedDB)
	const broken = "SELECT * FROM no_such_table"

	_, err := routed.ExecContext(t.Context(), broken)
	require.ErrorContains(t, err, "no such table")

	rows, err := routed.QueryContext(t.Context(), broken)
	require.ErrorContains(t, err, "no such table")
	if err == nil {
		defer rows.Close()
		require.ErrorContains(t, rows.Err(), "no such table")
	}
	require.ErrorContains(t, routed.QueryRowContext(t.Context(), broken).Scan(new(string)), "no such table")
	require.Zero(t, cachedStatements(routed), "a statement that fails to prepare is not cached")
}

// TestRoutedAfterReleaseReportsClosed pins the pool's lifetime: a routed
// DBTX kept past its Release reports the closed database instead of
// handing out a statement from a dead cache.
func TestRoutedAfterReleaseReportsClosed(t *testing.T) {
	t.Parallel()

	conn, _, dataDir := newRoutedQueries(t)
	routed := Routed(conn).(routedDB)
	require.NoError(t, countRows(t, routed, "SELECT count(*) FROM sessions"))
	require.Positive(t, cachedStatements(routed))

	require.NoError(t, Release(dataDir))

	require.ErrorContains(t, countRows(t, routed, "SELECT count(*) FROM sessions"), "database is closed")
	require.ErrorContains(t, routed.QueryRowContext(t.Context(), "SELECT 1").Scan(new(int)), "database is closed")
}

// countRows runs query through routed and reports the error of draining it.
func countRows(t *testing.T, routed routedDB, query string) error {
	t.Helper()
	rows, err := routed.QueryContext(t.Context(), query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

// BenchmarkRoutedQueryRow measures a query through the routed DBTX against
// the same query on the bare pool, which prepares it on every call.
func BenchmarkRoutedQueryRow(b *testing.B) {
	dir := b.TempDir()
	conn, err := Connect(b.Context(), dir)
	require.NoError(b, err)
	b.Cleanup(func() { _ = Release(dir) })
	routed := Routed(conn).(routedDB)
	const read = "SELECT count(*) FROM sessions"

	// Warm the cache outside the timer.
	var n int
	require.NoError(b, routed.QueryRowContext(b.Context(), read).Scan(&n))

	b.ReportAllocs()
	b.Run("cached", func(b *testing.B) {
		for b.Loop() {
			require.NoError(b, routed.QueryRowContext(b.Context(), read).Scan(&n))
		}
	})
	b.Run("unprepared", func(b *testing.B) {
		for b.Loop() {
			require.NoError(b, conn.QueryRowContext(b.Context(), read).Scan(&n))
		}
	})
}
