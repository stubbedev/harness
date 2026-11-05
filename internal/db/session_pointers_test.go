package db

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

type sessionPointers struct {
	summaryMessageID, compactionSummary, boundaryID, agedID sql.NullString
}

func readSessionPointers(t *testing.T, conn *sql.DB, id string) sessionPointers {
	t.Helper()
	var p sessionPointers
	require.NoError(t, conn.QueryRowContext(t.Context(),
		"SELECT summary_message_id, compaction_summary, compaction_boundary_id, compaction_aged_id FROM sessions WHERE id = ?", id,
	).Scan(&p.summaryMessageID, &p.compactionSummary, &p.boundaryID, &p.agedID))
	return p
}

func null() sql.NullString { return sql.NullString{} }

func str(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// TestDeletingMessagesClearsSessionPointersToThem: no delete can leave a
// session pointing at a message that is gone. A compaction whose
// boundary goes takes its summary and aged mark with it.
func TestDeletingMessagesClearsSessionPointersToThem(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		deleted []string
		want    sessionPointers
	}{
		{"boundary", []string{"b"}, sessionPointers{str("a"), null(), null(), null()}},
		{"aged only", []string{"c"}, sessionPointers{str("a"), str("summary"), str("b"), null()}},
		{"summary message", []string{"a"}, sessionPointers{null(), str("summary"), str("b"), str("c")}},
		{"unrelated", []string{"d"}, sessionPointers{str("a"), str("summary"), str("b"), str("c")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conn, q, _ := newRoutedQueries(t)
			_, err := q.CreateSession(t.Context(), CreateSessionParams{ID: "s", Title: "s"})
			require.NoError(t, err)
			for _, id := range []string{"a", "b", "c", "d"} {
				_, err := q.CreateMessage(t.Context(), CreateMessageParams{ID: id, SessionID: "s", Role: "assistant", Parts: "[]"})
				require.NoError(t, err)
			}
			_, err = q.UpdateSessionCompaction(t.Context(), UpdateSessionCompactionParams{
				ID:                   "s",
				SummaryMessageID:     str("a"),
				CompactionSummary:    str("summary"),
				CompactionBoundaryID: str("b"),
				CompactionAgedID:     str("c"),
			})
			require.NoError(t, err)

			for _, id := range tc.deleted {
				require.NoError(t, q.DeleteMessage(t.Context(), id))
			}
			require.Equal(t, tc.want, readSessionPointers(t, conn, "s"))
		})
	}
}

// TestDeletingASessionWithPointersCascades: the pointer trigger runs for
// every message a session delete cascades to, and must not get in its way.
func TestDeletingASessionWithPointersCascades(t *testing.T) {
	t.Parallel()
	conn, q, _ := newRoutedQueries(t)
	_, err := q.CreateSession(t.Context(), CreateSessionParams{ID: "s", Title: "s"})
	require.NoError(t, err)
	_, err = q.CreateMessage(t.Context(), CreateMessageParams{ID: "b", SessionID: "s", Role: "assistant", Parts: "[]"})
	require.NoError(t, err)
	_, err = q.UpdateSessionCompaction(t.Context(), UpdateSessionCompactionParams{ID: "s", CompactionBoundaryID: str("b")})
	require.NoError(t, err)

	require.NoError(t, q.DeleteSessions(t.Context(), []string{"s"}))
	var n int
	require.NoError(t, conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM messages WHERE session_id = 's'").Scan(&n))
	require.Zero(t, n)
}

// TestMigrationRepairsDanglingSessionPointers: a database whose earlier
// deletes left session pointers at missing messages comes out of the
// migration with those pointers cleared, so the session's compacted
// history no longer refers to a row that is gone, and with every pointer
// at a message that exists left alone.
func TestMigrationRepairsDanglingSessionPointers(t *testing.T) {
	t.Parallel()
	conn, q, _ := newRoutedQueries(t)
	require.NoError(t, initGoose())
	require.NoError(t, goose.DownToContext(t.Context(), conn, "migrations", 20261105000000))

	for _, id := range []string{"dangling", "intact"} {
		_, err := q.CreateSession(t.Context(), CreateSessionParams{ID: id, Title: id})
		require.NoError(t, err)
	}
	_, err := q.CreateMessage(t.Context(), CreateMessageParams{ID: "kept", SessionID: "intact", Role: "assistant", Parts: "[]"})
	require.NoError(t, err)
	for id, target := range map[string]string{"dangling": "gone", "intact": "kept"} {
		_, err = q.UpdateSessionCompaction(t.Context(), UpdateSessionCompactionParams{
			ID:                   id,
			SummaryMessageID:     str(target),
			CompactionSummary:    str("summary"),
			CompactionBoundaryID: str(target),
			CompactionAgedID:     str(target),
		})
		require.NoError(t, err)
	}

	require.NoError(t, goose.UpContext(t.Context(), conn, "migrations"))

	require.Equal(t, sessionPointers{null(), null(), null(), null()}, readSessionPointers(t, conn, "dangling"))
	require.Equal(t, sessionPointers{str("kept"), str("summary"), str("kept"), str("kept")}, readSessionPointers(t, conn, "intact"))
}
