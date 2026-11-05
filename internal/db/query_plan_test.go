package db

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// queryPlan returns the detail lines of the plan SQLite picks for query.
func queryPlan(t *testing.T, conn *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(t, err)
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())
	return details
}

// requireNoFullScan fails when a plan scans one of the tables whole.
func requireNoFullScan(t *testing.T, details []string) {
	t.Helper()
	plan := strings.Join(details, "\n")
	for _, detail := range details {
		for _, table := range []string{"sessions", "messages", "checkpoints", "m", "child"} {
			require.NotEqual(t, "SCAN "+table, detail, "full scan in plan:\n%s", plan)
		}
	}
}

// TestHotQueriesUseTheirIndexes pins the plans of the session-list,
// session-tree, prompt-history, message-write and checkpoint queries, so
// a schema or query change that turns one back into a table scan or a
// full sort fails here.
func TestHotQueriesUseTheirIndexes(t *testing.T) {
	t.Parallel()
	conn, _, _ := newRoutedQueries(t)

	cases := []struct {
		name  string
		query string
		args  []any
		index string
		// sorts is set where the plan may sort the rows of one session.
		sorts bool
	}{
		{"ListSessions", listSessions, nil, "idx_sessions_parent_updated_at", false},
		{"GetLastSession", getLastSession, nil, "idx_sessions_parent_updated_at", false},
		{"ListChildSessions", listChildSessions, []any{"p"}, "idx_sessions_parent_updated_at", false},
		{"ListSessionTree", listSessionTree, []any{"p"}, "idx_sessions_parent_updated_at", false},
		{"ListFilesBySessionWithChildren", listFilesBySessionWithChildren, []any{"p", "p"}, "idx_sessions_parent_updated_at", true},
		{"ListSessionPromptHistory", listSessionPromptHistory, []any{"p", 10}, "idx_messages_user_session_created_at", false},
		{"ListPromptHistory", listPromptHistory, []any{10}, "idx_messages_user_created_at", false},
		{"ListUserMessagesBySession", listUserMessagesBySession, []any{"p"}, "idx_messages_user_session_created_at", false},
		{"ListMessagesBySession", listMessagesBySession, []any{"p"}, "idx_messages_session_created_at", false},
		{"ListMessagesBySessionFrom", listMessagesBySessionFrom, []any{"p", "m"}, "idx_messages_session_created_at", false},
		{"UpdateMessage", updateMessage, []any{"[]", nil, nil, nil, nil, nil, 1, "m"}, "sqlite_autoindex_messages_1", false},
		{"DeleteMessage", deleteMessage, []any{"m"}, "sqlite_autoindex_messages_1", false},
		{"DeleteMessagesFrom", deleteMessagesFrom, []any{"p", "m"}, "idx_messages_session_created_at", false},
		// A session holds one checkpoint per prompt, so sorting them is
		// bounded by the length of one conversation.
		{"ListCheckpointsBySession", listCheckpointsBySession, []any{"p"}, "sqlite_autoindex_checkpoints_2", true},
		{"GetCheckpointByMessage", getCheckpointByMessage, []any{"m"}, "idx_checkpoints_message_id", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			details := queryPlan(t, conn, tc.query, tc.args...)
			plan := strings.Join(details, "\n")
			require.Contains(t, plan, tc.index)
			if !tc.sorts {
				require.NotContains(t, plan, "USE TEMP B-TREE FOR ORDER BY")
			}
			requireNoFullScan(t, details)
		})
	}
}

// TestSessionPointerTriggerSeeksTheSession pins that the trigger run for
// every deleted message finds its session by primary key instead of
// scanning sessions. Its statement is read back from the schema, with
// the old row's columns bound as parameters.
func TestSessionPointerTriggerSeeksTheSession(t *testing.T) {
	t.Parallel()
	conn, _, _ := newRoutedQueries(t)

	var trigger string
	require.NoError(t, conn.QueryRowContext(t.Context(),
		"SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'clear_session_pointers_on_message_delete'").Scan(&trigger))
	_, body, ok := strings.Cut(trigger, "BEGIN")
	require.True(t, ok, "trigger has no body:\n%s", trigger)
	end := strings.LastIndex(body, "END")
	require.Positive(t, end, "trigger has no end:\n%s", trigger)
	body = body[:end]
	stmt := strings.NewReplacer("old.session_id", ":session_id", "old.id", ":message_id").Replace(body)

	details := queryPlan(t, conn, stmt, sql.Named("session_id", "s"), sql.Named("message_id", "m"))
	require.Contains(t, strings.Join(details, "\n"), "sqlite_autoindex_sessions_1")
	requireNoFullScan(t, details)
}

// TestRedundantIndexesAreDropped pins that single-column indexes whose
// column already leads another index are gone: the wider index serves
// their lookups and every extra index taxes writes.
func TestRedundantIndexesAreDropped(t *testing.T) {
	t.Parallel()
	conn, _, _ := newRoutedQueries(t)
	for _, index := range []string{"idx_messages_session_id", "idx_checkpoints_session_id"} {
		var n int
		require.NoError(t, conn.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&n))
		require.Zero(t, n, index)
	}
}
