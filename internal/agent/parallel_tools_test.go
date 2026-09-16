package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

// probeTool blocks each call until `want` calls have started, so a test
// can tell whether the calls of one step ran together or one after the
// other. Sequential execution never reaches `want` started calls at once
// and times out into a plain result instead of deadlocking.
type probeTool struct {
	fantasy.AgentTool
	mu      sync.Mutex
	started int
	peak    atomic.Int32
	inside  atomic.Int32
	all     chan struct{}
	want    int
}

func newProbeTool(t *testing.T, want int, parallel bool) *probeTool {
	t.Helper()
	p := &probeTool{all: make(chan struct{}), want: want}
	type args struct {
		N int `json:"n"`
	}
	run := func(ctx context.Context, in args, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
		now := p.inside.Add(1)
		defer p.inside.Add(-1)
		for {
			prev := p.peak.Load()
			if now <= prev || p.peak.CompareAndSwap(prev, now) {
				break
			}
		}
		p.mu.Lock()
		p.started++
		if p.started == p.want {
			close(p.all)
		}
		p.mu.Unlock()
		select {
		case <-p.all:
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
		}
		return fantasy.NewTextResponse("probed"), nil
	}
	if parallel {
		p.AgentTool = fantasy.NewParallelAgentTool("probe", "probe", run)
	} else {
		p.AgentTool = fantasy.NewAgentTool("probe", "probe", run)
	}
	return p
}

func probeAgent(t *testing.T, env fakeEnv, probe fantasy.AgentTool) SessionAgent {
	t.Helper()
	model := newScriptedModel(scriptedTurn{calls: []scriptedCall{
		{name: "probe", input: map[string]any{"n": 1}},
		{name: "probe", input: map[string]any{"n": 2}},
		{name: "probe", input: map[string]any{"n": 3}},
	}})
	return NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{
			Model:      model,
			CatalogCfg: catalog.Model{ID: "large", ContextWindow: 1_000_000, DefaultMaxTokens: 1000},
			ModelCfg:   config.SelectedModel{Model: "large", Provider: "scripted"},
		},
		SmallModel:   Model{Model: textModel("title")},
		SystemPrompt: "system",
		Sessions:     env.sessions,
		Messages:     env.messages,
		Tools:        []fantasy.AgentTool{probe},
	})
}

// Read-only tools a step calls together run together: three parallel
// probes are all in flight at once.
func TestParallelToolsRunTogetherWithinAStep(t *testing.T) {
	env := testEnv(t)
	probe := newProbeTool(t, 3, true)
	sess, err := env.sessions.Create(t.Context(), "parallel")
	require.NoError(t, err)

	_, err = probeAgent(t, env, probe).Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "probe"})
	require.NoError(t, err)
	require.Equal(t, int32(3), probe.peak.Load(), "all three calls must overlap")
}

// Tools not marked parallel keep running one at a time, in order.
func TestSequentialToolsRunOneAtATime(t *testing.T) {
	env := testEnv(t)
	probe := newProbeTool(t, 3, false)
	sess, err := env.sessions.Create(t.Context(), "sequential")
	require.NoError(t, err)

	_, err = probeAgent(t, env, probe).Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "probe"})
	require.NoError(t, err)
	require.Equal(t, int32(1), probe.peak.Load(), "sequential tools never overlap")
}
