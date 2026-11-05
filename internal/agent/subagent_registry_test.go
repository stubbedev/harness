package agent

import (
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/subagents"
)

func TestSubagentRegistryBlockingRun(t *testing.T) {
	t.Parallel()

	r := newSubagentRegistry()
	cancelled := 0
	unregister := r.register("child", func() { cancelled++ })

	require.True(t, r.isRunning("child"))
	require.True(t, r.inbox.isSteerable("child"))
	_, routed := r.lookupByChild("child")
	require.False(t, routed, "a blocking run must not route live")
	require.True(t, r.cancel("child"))
	require.Equal(t, 1, cancelled)

	unregister()
	require.False(t, r.isRunning("child"))
	require.False(t, r.inbox.isSteerable("child"))
	require.False(t, r.cancel("child"))
	require.Equal(t, 1, cancelled)
}

func TestSubagentRegistryBackgroundRun(t *testing.T) {
	t.Parallel()

	r := newSubagentRegistry()
	cancelled := 0
	run, unregister := r.registerBackground("parent", "child", "fast", func() { cancelled++ })

	require.Equal(t, "child", run.childSession)
	require.Equal(t, "parent", run.parentSession)
	require.Equal(t, "fast", run.agentName)
	require.NotEmpty(t, run.handle)

	byChild, ok := r.lookupByChild("child")
	require.True(t, ok)
	require.Same(t, run, byChild)
	byHandle, ok := r.lookupByHandle("parent", run.handle)
	require.True(t, ok)
	require.Same(t, run, byHandle)
	_, ok = r.lookupByHandle("other", run.handle)
	require.False(t, ok, "a handle must not resolve for another session")
	require.Equal(t, []string{run.handle}, r.handles("parent"))
	require.Empty(t, r.handles("other"))
	require.True(t, r.isRunning("child"))
	require.True(t, r.inbox.isSteerable("child"))

	unregister()
	_, ok = r.lookupByChild("child")
	require.False(t, ok, "unregister must drop the live route")
	require.False(t, r.isRunning("child"), "unregister must drop the cancel")
	require.False(t, r.inbox.isSteerable("child"), "unregister must close steering")
	require.False(t, r.cancel("child"))
	require.Zero(t, cancelled)
	kept, ok := r.lookupByHandle("parent", run.handle)
	require.True(t, ok, "a finished run stays collectable by handle")
	require.Same(t, run, kept)
}

func TestSubagentRegistryHandlesAreUniqueAndSorted(t *testing.T) {
	t.Parallel()

	r := newSubagentRegistry()
	seen := map[string]bool{}
	for range 20 {
		run, _ := r.registerBackground("parent", "child-"+newSubagentHandle(), "fast", func() {})
		require.False(t, seen[run.handle])
		seen[run.handle] = true
	}
	handles := r.handles("parent")
	require.Len(t, handles, 20)
	require.IsNonDecreasing(t, handles)
}

func TestSubagentRegistryDiscard(t *testing.T) {
	t.Parallel()

	r := newSubagentRegistry()
	cancelled := false
	run, _ := r.registerBackground("parent", "child", "fast", func() { cancelled = true })

	r.discard("child")
	require.True(t, cancelled)
	_, ok := r.lookupByHandle("parent", run.handle)
	require.False(t, ok)
	require.Empty(t, r.handles("parent"))
}

func TestSubagentRegistryStaleUnregisterKeepsReRegisteredRun(t *testing.T) {
	t.Parallel()

	r := newSubagentRegistry()
	first := r.register("child", func() {})
	first()
	second := r.register("child", func() {})

	first()
	require.True(t, r.isRunning("child"), "a repeated unregister must not drop a later registration")
	second()
	require.False(t, r.isRunning("child"))
}

func TestSubagentRegistryCancelAll(t *testing.T) {
	t.Parallel()

	r := newSubagentRegistry()
	cancelled := map[string]bool{}
	r.register("blocking", func() { cancelled["blocking"] = true })
	r.registerBackground("parent", "background", "fast", func() { cancelled["background"] = true })

	r.cancelAll()
	require.Equal(t, map[string]bool{"blocking": true, "background": true}, cancelled)
}

func TestCoordinatorWithoutConstructorFailsLoudly(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() { (&coordinator{}).DrainSubagentInbox("s") })
	require.Panics(t, func() { (&coordinator{}).NotifyQueueArrival("s") })
	require.Panics(t, func() { (&coordinator{}).recordSubagentMessage("child", "m") })
	require.Panics(t, func() { (&coordinator{}).builtinToolExpanded("view") })
}

func TestCoordinatorForTestAllocatesRegistries(t *testing.T) {
	t.Parallel()

	c := newCoordinatorForTest(&coordinator{})
	require.Nil(t, c.DrainSubagentInbox("s"))
	c.NotifyQueueArrival("s")
	require.Equal(t, uint64(1), c.queueArrivalEpoch("s"))
	live, err := c.recordSubagentMessage("child", "m")
	require.NoError(t, err)
	require.False(t, live)
	require.Equal(t, []string{"m"}, c.drainSubagentMessages("child"))
	require.False(t, c.builtinToolExpanded("view"))
}

// TestSubagentRegistryForgetsADeletedSession pins the registry's bound:
// the records of a deleted session's background runs, and its inbox, go
// with it, a run still live is cancelled, and another session's records
// are untouched.
func TestSubagentRegistryForgetsADeletedSession(t *testing.T) {
	t.Parallel()

	r := newSubagentRegistry()
	gone, keep := "parent-gone", "parent-kept"
	finished, unregisterFinished := r.registerBackground(gone, "child-1", "fast", func() {})
	finished.finish(subagents.StatusCompleted, fantasy.NewTextResponse("report"))
	unregisterFinished()
	liveCancelled := false
	r.registerBackground(gone, "child-2", "fast", func() { liveCancelled = true })
	kept, _ := r.registerBackground(keep, "child-3", "fast", func() {})
	require.NoError(t, r.inbox.record(gone, SubagentInboxMessage{Text: "unread"}))

	r.forgetSession(gone)

	assert.Empty(t, r.handles(gone))
	_, found := r.lookupByHandle(gone, finished.handle)
	assert.False(t, found, "the finished run's record is gone")
	assert.True(t, liveCancelled, "a live run of the deleted session is cancelled")
	assert.Empty(t, r.inbox.drain(gone))
	assert.Equal(t, []string{kept.handle}, r.handles(keep))
}

// TestBackgroundRunReleasesItsResultOnceCollected pins that a collected
// result is not kept: a later wait only reports that it was returned.
func TestBackgroundRunReleasesItsResultOnceCollected(t *testing.T) {
	t.Parallel()

	r := newSubagentRegistry()
	run, _ := r.registerBackground("p", "c", "fast", func() {})
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("a long report"))

	_, _, result, _, delivered := run.take()
	require.True(t, delivered)
	assert.Equal(t, "a long report", result)

	_, _, result, _, delivered = run.take()
	assert.False(t, delivered)
	assert.Empty(t, result)
	_, _, kept, _ := run.snapshot()
	assert.Empty(t, kept, "the text is released once handed back")
}

// TestDeletingASessionForgetsItsBackgroundState drives the coordinator
// end to end: deleting a session through the session service drops what
// the coordinator kept for it.
func TestDeletingASessionForgetsItsBackgroundState(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	coord := newTestCoordinator(t, env, "test-provider", offlineTestProvider("model-a"))
	coord.forgetDeletedSessions(t.Context())
	sess, err := env.sessions.Create(t.Context(), "parent")
	require.NoError(t, err)
	run, _ := coord.subagentRuns.registerBackground(sess.ID, "child", "fast", func() {})
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("report"))
	_, err = coord.timers.arm(sess.ID, minTimerDuration)
	require.NoError(t, err)

	require.NoError(t, env.sessions.Delete(t.Context(), sess.ID))

	require.Eventually(t, func() bool {
		return len(coord.subagentRuns.handles(sess.ID)) == 0 && len(coord.timers.records(sess.ID)) == 0
	}, 5*time.Second, 10*time.Millisecond, "the deleted session's runs and timers are forgotten")
}
