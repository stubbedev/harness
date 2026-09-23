package hooks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

// stubDispatcher answers one event with a fixed result.
type stubDispatcher struct {
	event  string
	name   string
	result HookResult
	calls  int
}

func (d *stubDispatcher) Has(event string) bool { return event == d.event }

func (d *stubDispatcher) Dispatch(_ context.Context, _ EventContext) []DispatchResult {
	d.calls++
	return []DispatchResult{{Name: d.name, Result: d.result}}
}

func TestRegistryRunsDispatcherWithoutConfiguredHooks(t *testing.T) {
	t.Parallel()

	dispatcher := &stubDispatcher{
		event:  EventPreToolUse,
		name:   "guard:PreToolUse",
		result: HookResult{Decision: DecisionDeny, Reason: "denied in lua"},
	}
	cfg := &config.Config{}
	require.NoError(t, cfg.ValidateHooks())
	r := NewRegistry(config.NewTestStore(cfg), t.TempDir(), t.TempDir(), dispatcher)

	require.True(t, r.Has(EventPreToolUse))
	require.False(t, r.Has(EventStop))

	res, err := r.Run(context.Background(), EventContext{Event: EventPreToolUse, ToolName: "shell"})
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, res.Decision)
	require.Equal(t, "denied in lua", res.Reason)
	require.Equal(t, 1, res.HookCount)
	require.Len(t, res.Hooks, 1)
	require.Equal(t, "guard:PreToolUse", res.Hooks[0].Name)

	// An event no dispatcher claims never reaches it.
	_, err = r.Run(context.Background(), EventContext{Event: EventStop})
	require.NoError(t, err)
	require.Equal(t, 1, dispatcher.calls)
}

func TestRegistryAggregatesShellHooksAndDispatchers(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			EventPreToolUse: {{Command: `echo '{"context":"from a shell hook"}'`}},
		},
	}
	require.NoError(t, cfg.ValidateHooks())

	dispatcher := &stubDispatcher{
		event:  EventPreToolUse,
		name:   "guard:PreToolUse",
		result: HookResult{Decision: DecisionDeny, Reason: "denied in lua"},
	}
	r := NewRegistry(config.NewTestStore(cfg), t.TempDir(), t.TempDir(), dispatcher)

	res, err := r.Run(context.Background(), EventContext{Event: EventPreToolUse, ToolName: "shell"})
	require.NoError(t, err)

	// Both sources ran, and the deny from the dispatcher wins over the
	// shell hook that only added context.
	require.Equal(t, 2, res.HookCount)
	require.Equal(t, DecisionDeny, res.Decision)
	require.Equal(t, "denied in lua", res.Reason)
	require.Equal(t, "from a shell hook", res.Context)
}

func TestNewRegistryIgnoresNilDispatcher(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	require.NoError(t, cfg.ValidateHooks())
	r := NewRegistry(config.NewTestStore(cfg), t.TempDir(), t.TempDir(), nil)

	require.False(t, r.Has(EventPreToolUse))
}
