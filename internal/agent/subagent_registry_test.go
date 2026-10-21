package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
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
