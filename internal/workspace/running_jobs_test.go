package workspace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/app"
	"github.com/stubbedev/harness/internal/config"
)

// stubJobCoordinator serves a fixed job feed. Only RunningJobs is
// reached; the embedded nil Coordinator satisfies the rest of the
// interface.
type stubJobCoordinator struct {
	agent.Coordinator
	jobs []agent.JobInfo
}

func (s stubJobCoordinator) RunningJobs(string) []agent.JobInfo { return s.jobs }

// TestAppWorkspace_RunningJobs_Empty verifies that a workspace without a
// coordinator reports no jobs.
func TestAppWorkspace_RunningJobs_Empty(t *testing.T) {
	t.Parallel()

	w := &AppWorkspace{
		app:   &app.App{},
		store: config.NewTestStore(&config.Config{}),
	}

	require.Nil(t, w.RunningJobs("session-1"))
}

// TestAppWorkspace_RunningJobs_Projects verifies that a coordinator's job
// snapshots reach the frontend with the fields the strip renders.
func TestAppWorkspace_RunningJobs_Projects(t *testing.T) {
	t.Parallel()

	started := time.Now().Add(-90 * time.Second)
	coord := stubJobCoordinator{jobs: []agent.JobInfo{{
		ID:        "job-1",
		Command:   "go test ./...",
		State:     agent.JobRunning,
		StartedAt: started,
		Elapsed:   90 * time.Second,
	}}}
	w := &AppWorkspace{
		app:   &app.App{AgentCoordinator: coord},
		store: config.NewTestStore(&config.Config{}),
	}

	got := w.RunningJobs("session-1")
	require.Len(t, got, 1)
	require.Equal(t, "job-1", got[0].ID)
	require.Equal(t, "go test ./...", got[0].Command)
	require.Equal(t, agent.JobRunning, got[0].State)
	require.Equal(t, started, got[0].StartedAt)
	require.Equal(t, 90*time.Second, got[0].Elapsed)
}
