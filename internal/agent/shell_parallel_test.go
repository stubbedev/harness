package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agent/tools"
)

// recordingShellTool records the session name each call resolved to, then
// runs them concurrently so a test can see which terminal each went to.
type recordingShellTool struct {
	parallel bool
	seen     []string
	mu       sync.Mutex
	peak     atomic.Int32
	inside   atomic.Int32
	release  chan struct{}
}

func (r *recordingShellTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        tools.ShellToolName,
		Description: "test",
		Parameters:  map[string]any{},
		Required:    []string{},
		Parallel:    r.parallel,
	}
}

func (r *recordingShellTool) ProviderOptions() fantasy.ProviderOptions {
	return fantasy.ProviderOptions{}
}
func (r *recordingShellTool) SetProviderOptions(fantasy.ProviderOptions) {}

func (r *recordingShellTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	r.inside.Add(1)
	defer r.inside.Add(-1)
	for {
		prev := r.peak.Load()
		if now := r.inside.Load(); now <= prev || r.peak.CompareAndSwap(prev, now) {
			break
		}
	}
	var params tools.ShellParams
	_ = json.Unmarshal([]byte(call.Input), &params)
	r.mu.Lock()
	r.seen = append(r.seen, params.Session)
	r.mu.Unlock()
	if r.release != nil {
		<-r.release
	}
	return fantasy.NewTextResponse("ok"), nil
}

func (r *recordingShellTool) sessions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.seen))
	copy(out, r.seen)
	return out
}

func runSpreadShell(t *testing.T, tool fantasy.AgentTool, ctx context.Context, session string) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(struct {
		Session string `json:"session,omitempty"`
	}{Session: session})
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "c", Name: tools.ShellToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

// The first default shell call keeps the stable default; later ones each
// get their own terminal, so they can run in parallel instead of queuing
// on one.
func TestShellSessionSpread_DistributesDefaults(t *testing.T) {
	rec := &recordingShellTool{parallel: true, release: make(chan struct{})}
	tool := newShellSessionSpreadTool(rec)
	ctx := withShellSessionCounter(context.Background())

	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			runSpreadShell(t, tool, ctx, "")
		})
	}
	// All calls are in flight before we release them.
	require.Eventually(t, func() bool { return rec.peak.Load() == 3 }, 300*time.Millisecond, 5*time.Millisecond,
		"all three calls must overlap")
	close(rec.release)
	wg.Wait()

	sessions := rec.sessions()
	require.ElementsMatch(t, []string{"", "p1", "p2"}, sessions,
		"first default keeps the stable default; later ones spread")
}

// A call the model named explicitly is left alone even in a spread batch:
// a named session is the model's intent to reuse one terminal.
func TestShellSessionSpread_RespectsExplicitNames(t *testing.T) {
	rec := &recordingShellTool{parallel: true}
	tool := newShellSessionSpreadTool(rec)
	ctx := withShellSessionCounter(context.Background())

	runSpreadShell(t, tool, ctx, "srv")
	runSpreadShell(t, tool, ctx, "build")
	runSpreadShell(t, tool, ctx, "")

	require.Equal(t, []string{"srv", "build", ""}, rec.sessions(),
		"explicit names are preserved; only an empty default spreads")
}

// Without a per-step counter (direct callers, tests) spreading is a
// no-op: every call falls back to the stable default, the pre-parallel
// behavior.
func TestShellSessionSpread_NoCounterKeepsStableDefault(t *testing.T) {
	rec := &recordingShellTool{parallel: true}
	tool := newShellSessionSpreadTool(rec)
	// Plain context, no counter attached.
	runSpreadShell(t, tool, context.Background(), "")
	runSpreadShell(t, tool, context.Background(), "")

	require.Equal(t, []string{"", ""}, rec.sessions(),
		"without a step counter nothing is spread")
}
