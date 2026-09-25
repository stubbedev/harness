package presence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := New(t.TempDir())
	require.NoError(t, err)
	return r
}

// publish writes another instance's record directly, the way a peer
// process would, and returns its registry-facing path.
func publish(t *testing.T, dir string, rec Record) string {
	t.Helper()
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	path := filepath.Join(dir, rec.ID+".json")
	require.NoError(t, os.WriteFile(path, b, 0o644))
	return path
}

func TestStartPublishesAndStopRemoves(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)
	r.Start(t.Context())
	entries, err := os.ReadDir(r.dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Contains(t, entries[0].Name(), r.Self().ID)

	r.Stop()
	entries, err = os.ReadDir(r.dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestPeersFiltersSelfAndStale(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)
	now := time.Now()
	live := Record{ID: "live", PID: 1, Started: now.Add(-time.Minute), Beat: now}
	stale := Record{ID: "stale", PID: 2, Started: now.Add(-time.Hour), Beat: now.Add(-time.Minute)}

	publish(t, r.dir, live)
	publish(t, r.dir, stale)
	r.Start(t.Context())

	peers := r.Peers()
	require.Len(t, peers, 1)
	require.Equal(t, "live", peers[0].ID)
}

func TestPeersCollectsLongDead(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)
	now := time.Now()
	dead := Record{ID: "dead", PID: 1, Started: now.Add(-time.Hour), Beat: now.Add(-30 * time.Minute)}
	path := publish(t, r.dir, dead)

	r.Peers()
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "long-dead record should be collected")
}

func TestPeersDoesNotCollectBarelyStale(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)
	now := time.Now()
	delayed := Record{ID: "delayed", PID: 1, Started: now.Add(-time.Hour), Beat: now.Add(-time.Minute)}
	path := publish(t, r.dir, delayed)

	r.Peers()
	_, err := os.Stat(path)
	require.NoError(t, err, "record past stale but under gc threshold must survive")
}

func TestTouchRecordsMostRecentFirst(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)
	base := time.Now()
	r.now = func() time.Time { return base }
	r.Touch("b.go", "a.go", "b.go")

	// Equal timestamps tie-break by path; the duplicate b.go collapses.
	require.Equal(t, []string{"a.go", "b.go"}, activityPaths(r.Self().Files))
}

func TestTouchAgesOut(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)
	base := time.Now()
	current := base
	r.now = func() time.Time { return current }
	r.Touch("old.go")
	current = base.Add(r.activityWindow + time.Second)
	r.Touch("new.go")

	require.Equal(t, []string{"new.go"}, activityPaths(r.Self().Files))
}

func TestBeginEndTurnBusy(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)
	r.BeginTurn()
	require.True(t, r.Self().Busy)
	r.BeginTurn()
	r.EndTurn()
	require.True(t, r.Self().Busy)
	r.EndTurn()
	require.False(t, r.Self().Busy)
}

func TestPeersTrimsFileWindow(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)
	now := time.Now()
	rec := Record{
		ID:      "live",
		PID:     1,
		Started: now.Add(-time.Minute),
		Beat:    now,
		Files: []Activity{
			{Path: "fresh.go", At: now.Add(-time.Second)},
			{Path: "old.go", At: now.Add(-r.activityWindow - time.Second)},
		},
	}
	publish(t, r.dir, rec)

	peers := r.Peers()
	require.Len(t, peers, 1)
	require.Equal(t, []string{"fresh.go"}, activityPaths(peers[0].Files))
}

func TestContentionWarnsThenRateLimits(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)
	now := time.Now()
	publish(t, r.dir, Record{
		ID:      "live",
		PID:     7,
		Title:   "fixing the tui",
		Started: now.Add(-time.Minute),
		Beat:    now,
		Files:   []Activity{{Path: "internal/ui/ui.go", At: now.Add(-10 * time.Second)}},
	})

	first := r.Contention("internal/ui/ui.go")
	require.Contains(t, first, "pid 7")
	require.Contains(t, first, `"fixing the tui"`)
	require.Contains(t, first, "internal/ui/ui.go")

	require.Empty(t, r.Contention("internal/ui/ui.go"), "rate-limited within the window")
	require.Empty(t, r.Contention("other.go"), "untouched file")
}

func TestContentionSilentWhenPeerGone(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)
	now := time.Now()
	publish(t, r.dir, Record{
		ID:      "stale",
		PID:     7,
		Started: now.Add(-time.Hour),
		Beat:    now.Add(-time.Minute),
		Files:   []Activity{{Path: "x.go", At: now.Add(-time.Minute)}},
	})
	require.Empty(t, r.Contention("x.go"))
}

func activityPaths(files []Activity) []string {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return paths
}

func TestRelative(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, "internal/presence/a.go", relative(filepath.Join(wd, "internal/presence/a.go")))
	require.Equal(t, "a/b.go", relative("a/b.go"))
}
