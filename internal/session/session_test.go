package session

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/db"
)

func TestEstimatedUsageStateSurvivesFetchModifySave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	created.PromptTokens = 100
	created.CompletionTokens = 50
	created.EstimatedUsage = true

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.EstimatedUsage)

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, fetched.EstimatedUsage)

	fetched.Todos = []Todo{{
		Content:    "Check estimate state",
		Status:     TodoStatusInProgress,
		ActiveForm: "Checking estimate state",
	}}

	updated, err := sessions.Save(t.Context(), fetched)
	require.NoError(t, err)
	require.True(t, updated.EstimatedUsage)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, refetched.EstimatedUsage)
}

func TestEstimatedUsageStateCanBeClearedByExplicitSave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	created.PromptTokens = 100
	created.CompletionTokens = 50
	created.EstimatedUsage = true

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.EstimatedUsage)

	saved.EstimatedUsage = false
	updated, err := sessions.Save(t.Context(), saved)
	require.NoError(t, err)
	require.False(t, updated.EstimatedUsage)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.False(t, refetched.EstimatedUsage)
}

func TestListChildSessions_ReturnsOnlyDirectChildren(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	parent, err := sessions.Create(t.Context(), "parent title")
	require.NoError(t, err)

	child1, err := sessions.CreateTaskSession(t.Context(), "tool-call-1", parent.ID, "child 1 title")
	require.NoError(t, err)

	child2, err := sessions.CreateTaskSession(t.Context(), "tool-call-2", parent.ID, "child 2 title")
	require.NoError(t, err)

	_, err = sessions.CreateTaskSession(t.Context(), "tool-call-3", child1.ID, "grandchild title")
	require.NoError(t, err)

	children, err := sessions.ListChildSessions(t.Context(), parent.ID)
	require.NoError(t, err)
	require.Len(t, children, 2)

	ids := []string{children[0].ID, children[1].ID}
	require.ElementsMatch(t, []string{child1.ID, child2.ID}, ids)
}

func TestListChildSessions_NoChildren(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	lonely, err := sessions.Create(t.Context(), "lonely title")
	require.NoError(t, err)

	children, err := sessions.ListChildSessions(t.Context(), lonely.ID)
	require.NoError(t, err)
	require.Len(t, children, 0)
}

func TestDelete_RemovesDescendantsAndGetLastSkipsChildren(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	parent, err := sessions.Create(t.Context(), "parent title")
	require.NoError(t, err)
	child, err := sessions.CreateTaskSession(t.Context(), "tool-call-1", parent.ID, "child")
	require.NoError(t, err)
	grandchild, err := sessions.CreateTaskSession(t.Context(), "tool-call-2", child.ID, "grandchild")
	require.NoError(t, err)

	// The newest session is a child; "continue last" must not resume it.
	last, err := sessions.GetLast(t.Context())
	require.NoError(t, err)
	require.Equal(t, parent.ID, last.ID)

	require.NoError(t, sessions.Delete(t.Context(), parent.ID))
	for _, id := range []string{parent.ID, child.ID, grandchild.ID} {
		_, err := sessions.Get(t.Context(), id)
		require.Error(t, err, id)
	}
}

// TestNarrowUpdatesKeepConcurrentWrites pins the reason usage and
// compaction have their own updates: a rename or a sub-agent cost roll-up
// landing between an agent's read and its write must survive it, which a
// whole-row Save could not guarantee.
func TestNarrowUpdatesKeepConcurrentWrites(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "before")
	require.NoError(t, err)

	require.NoError(t, sessions.Rename(t.Context(), created.ID, "renamed"))
	require.NoError(t, sessions.AddCost(t.Context(), created.ID, 2))

	estimated := false
	got, err := sessions.RecordUsage(t.Context(), created.ID, Usage{CostDelta: 1, PromptTokens: 10, Estimated: &estimated})
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Title)
	require.InDelta(t, 3.0, got.Cost, 1e-9)
	require.Equal(t, int64(10), got.PromptTokens)

	got, err = sessions.RecordUsage(t.Context(), created.ID, Usage{CompletionTokens: 5})
	require.NoError(t, err)
	require.Equal(t, int64(10), got.PromptTokens, "an unmeasured counter is left alone")
	require.Equal(t, int64(5), got.CompletionTokens)

	got, err = sessions.SetCompaction(t.Context(), created.ID, Compaction{Summary: "s", BoundaryID: "b"}, &TokenCounts{Prompt: 7, Estimated: true})
	require.NoError(t, err)
	require.Equal(t, "s", got.CompactionSummary)
	require.Equal(t, int64(7), got.PromptTokens)
	require.Equal(t, int64(0), got.CompletionTokens)
	require.True(t, got.EstimatedUsage)
	require.InDelta(t, 3.0, got.Cost, 1e-9, "compaction leaves the cost alone")
	require.Equal(t, "renamed", got.Title)
}
