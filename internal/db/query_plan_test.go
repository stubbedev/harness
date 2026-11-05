package db

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHotQueriesUseTheirIndexes pins the plans of the session-list,
// session-tree and prompt-history queries, so a schema or query change
// that turns one back into a table scan or a full sort fails here.
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rows, err := conn.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+tc.query, tc.args...)
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

			plan := strings.Join(details, "\n")
			require.Contains(t, plan, tc.index)
			if !tc.sorts {
				require.NotContains(t, plan, "USE TEMP B-TREE FOR ORDER BY")
			}
			for _, detail := range details {
				for _, table := range []string{"sessions", "messages", "m", "child"} {
					require.NotEqual(t, "SCAN "+table, detail, "full scan in plan:\n%s", plan)
				}
			}
		})
	}
}

// TestRedundantMessageSessionIndexIsDropped pins that the single-column
// session index is gone: idx_messages_session_created_at serves its
// lookups and every extra index taxes message writes.
func TestRedundantMessageSessionIndexIsDropped(t *testing.T) {
	t.Parallel()
	conn, _, _ := newRoutedQueries(t)
	var n int
	require.NoError(t, conn.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_messages_session_id'").Scan(&n))
	require.Zero(t, n)
}
