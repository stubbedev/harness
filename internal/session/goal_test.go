package session

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/db"
)

func TestGoalSurvivesSaveAndClears(t *testing.T) {
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
	require.Nil(t, created.Goal)

	goal, err := NewGoal("  all tests pass  ", time.Unix(100, 0))
	require.NoError(t, err)
	require.NoError(t, sessions.SetGoal(t.Context(), created.ID, goal))

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, fetched.Goal.Active())
	require.Equal(t, "all tests pass", fetched.Goal.Condition)
	require.EqualValues(t, 100, fetched.Goal.CreatedAt)

	// A fetch-modify-save of a copy read before the goal was set must not
	// wipe it: Save does not write the goal column.
	created.Title = "renamed"
	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.Goal.Active())

	require.NoError(t, sessions.SetGoal(t.Context(), created.ID, nil))
	fetched, err = sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Nil(t, fetched.Goal)
	require.False(t, fetched.Goal.Active())
}

func TestNewGoalValidates(t *testing.T) {
	t.Parallel()

	_, err := NewGoal("   ", time.Now())
	require.Error(t, err)

	_, err = NewGoal(strings.Repeat("é", MaxGoalConditionLength+1), time.Now())
	require.Error(t, err)

	goal, err := NewGoal(strings.Repeat("é", MaxGoalConditionLength), time.Now())
	require.NoError(t, err)
	require.Equal(t, GoalActive, goal.Status)
}
