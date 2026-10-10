package db

import (
	"context"
	"database/sql"
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
