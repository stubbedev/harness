package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

func TestMaxConcurrentSubagents_Defaults(t *testing.T) {
	t.Parallel()

	require.Equal(t, DefaultMaxConcurrentSubagents, maxConcurrentSubagents(nil),
		"a nil store must fall back to the default rather than yielding a zero-capacity semaphore")

	// The cap is there to stop a runaway dispatch, not to ration ordinary
	// fan-out. A survey of a typical changed-file set must fit in one wave, or
	// the prompt's advice to dispatch one agent per piece turns into queueing.
	require.GreaterOrEqual(t, DefaultMaxConcurrentSubagents, 20,
		"the default must leave room for a real fan-out to run in a single wave")
}

func TestAcquireDispatchSlot_NilSemaphoreIsUncapped(t *testing.T) {
	t.Parallel()

	// Coordinators built directly in tests have no semaphore; dispatch must
	// still work rather than blocking on a nil channel forever.
	c := &coordinator{}
	for range 3 {
		release, err := c.acquireDispatchSlot(t.Context())
		require.NoError(t, err)
		require.NotNil(t, release)
		release()
	}
}

func TestAcquireDispatchSlot_BoundsConcurrency(t *testing.T) {
	t.Parallel()

	const limit = 2
	c := &coordinator{dispatchSem: make(chan struct{}, limit)}

	var (
		inFlight atomic.Int64
		peak     atomic.Int64
		wg       sync.WaitGroup
	)
	for range 12 {
		wg.Go(func() {
			release, err := c.acquireDispatchSlot(context.Background())
			if err != nil {
				return
			}
			defer release()
			now := inFlight.Add(1)
			for {
				old := peak.Load()
				if now <= old || peak.CompareAndSwap(old, now) {
					break
				}
			}
			inFlight.Add(-1)
		})
	}
	wg.Wait()

	require.LessOrEqual(t, peak.Load(), int64(limit), "more sub-agents ran at once than the limit allows")
	require.Len(t, c.dispatchSem, 0, "every slot must be released")
}

func TestAcquireDispatchSlot_HonoursContext(t *testing.T) {
	t.Parallel()

	c := &coordinator{dispatchSem: make(chan struct{}, 1)}
	release, err := c.acquireDispatchSlot(t.Context())
	require.NoError(t, err)
	t.Cleanup(release)

	// With the only slot held, a cancelled context must fail the acquire
	// instead of blocking the dispatch forever.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.acquireDispatchSlot(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestAcquireDispatchSlot_ReleaseIsIdempotent(t *testing.T) {
	t.Parallel()

	c := &coordinator{dispatchSem: make(chan struct{}, 1)}
	release, err := c.acquireDispatchSlot(t.Context())
	require.NoError(t, err)

	// runSubAgent's dispatch path defers release; a double call must not free
	// a slot it does not own and let an extra sub-agent through.
	release()
	release()
	require.Len(t, c.dispatchSem, 0)
}

// TestFastAgentConfigured pins the built-in fast agent: the small model, and
// the same read-and-edit, MCP-free tool set as the task agent. A fast agent
// that silently ran on the large model would make fan-out expensive rather
// than cheap, which is the whole reason it exists.
func TestFastAgentConfigured(t *testing.T) {
	t.Parallel()

	cfg, err := config.Init(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)

	agents := cfg.Config().Agents
	fast, ok := agents[config.AgentFast]
	require.True(t, ok, "the fast agent must always be configured")
	require.Equal(t, config.SelectedModelTypeSmall, fast.Model)
	require.Empty(t, fast.AllowedMCP, "the fast agent must not reach MCP servers")

	task := agents[config.AgentTask]
	require.Equal(t, task.AllowedTools, fast.AllowedTools, "fast is the task agent's tool set on a cheaper model")
	for _, tool := range []string{"edit", "write", "shell"} {
		require.Contains(t, fast.AllowedTools, tool,
			"built-in subagents read, edit and run commands in their own shell")
	}
	require.NotContains(t, fast.AllowedTools, "memory", "built-in subagents must not write memory")
}

func TestMaxConcurrentSubagents_ConfiguredAndClamped(t *testing.T) {
	t.Parallel()

	store, err := config.Init(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)
	opts := store.Config().Options

	require.Equal(t, DefaultMaxConcurrentSubagents, maxConcurrentSubagents(store),
		"an unset option must keep the default")

	limit := 3
	opts.MaxConcurrentSubagents = &limit
	require.Equal(t, 3, maxConcurrentSubagents(store))

	// Zero would make every dispatch block forever, so it is clamped rather
	// than honoured; disabling delegation is options.disabled_tools' job.
	zero := 0
	opts.MaxConcurrentSubagents = &zero
	require.Equal(t, 1, maxConcurrentSubagents(store))

	negative := -5
	opts.MaxConcurrentSubagents = &negative
	require.Equal(t, 1, maxConcurrentSubagents(store))
}
