package agent

import (
	"context"
	"strconv"
	"sync/atomic"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// shellSessionSpreadTool wraps the shell tool so several shell calls in
// one step run in parallel terminals instead of serializing on one. The
// shell tool is parallel, but calls that share a session name queue on
// that terminal's command lock: without spreading, a step that fires two
// shell calls both defaulting to "main" runs the second only after the
// first finishes, forfeiting the parallelism the parallel flag promises.
//
// Spreading keeps the stable default for the first default call - so the
// cwd, environment and credentials a single shell call relies on across
// turns still persist - and gives each later default call its own
// terminal. A call the model named explicitly is left alone: a named
// session is the model's intent to reuse one terminal, and two calls the
// model deliberately pointed at the same name should still meet there.
type shellSessionSpreadTool struct {
	inner fantasy.AgentTool
}

func newShellSessionSpreadTool(inner fantasy.AgentTool) fantasy.AgentTool {
	return &shellSessionSpreadTool{inner: inner}
}

func (s *shellSessionSpreadTool) Info() fantasy.ToolInfo { return s.inner.Info() }
func (s *shellSessionSpreadTool) ProviderOptions() fantasy.ProviderOptions {
	return s.inner.ProviderOptions()
}

func (s *shellSessionSpreadTool) SetProviderOptions(o fantasy.ProviderOptions) {
	s.inner.SetProviderOptions(o)
}

// MCP forwards the wrapped tool's server name so grouping built tools by
// MCP server still sees it through the wrapper.
func (s *shellSessionSpreadTool) MCP() string {
	if m, ok := s.inner.(interface{ MCP() string }); ok {
		return m.MCP()
	}
	return ""
}

func (s *shellSessionSpreadTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if counter := shellSessionCounter(ctx); counter != nil && call.Name == tools.ShellToolName {
		// An empty session is the default: the model left the terminal to
		// the tool. The first such call keeps the stable default; each
		// later one gets its own terminal so the calls run together.
		session := gjson.Get(call.Input, "session")
		if !session.Exists() || session.String() == "" {
			if idx := counter.Add(1) - 1; idx > 0 {
				if updated, err := sjson.Set(call.Input, "session", parallelSessionName(idx)); err == nil {
					call.Input = updated
				}
			}
		}
	}
	return s.inner.Run(ctx, call)
}

// parallelSessionName is the terminal name a later default shell call in
// a step gets. It must satisfy the shell tool's session name pattern
// ([A-Za-z0-9][A-Za-z0-9._-]{0,31}); "p1", "p2", ... do, and stay clear of
// the stable default ("main") and the short names a model is likely to
// pick itself.
func parallelSessionName(idx int64) string {
	return "p" + strconv.FormatInt(idx, 10)
}

// shellSessionCounterKey scopes a per-step shell session counter to the
// step context. The context is discarded with the step, so the counter is
// too: there is nothing to reap across steps.
type shellSessionCounterKey struct{}

// withShellSessionCounter attaches a fresh per-step counter to ctx. Each
// step gets its own (see stepToolContext), so the first default shell
// call in a step keeps the stable terminal and only later ones spread.
func withShellSessionCounter(ctx context.Context) context.Context {
	return context.WithValue(ctx, shellSessionCounterKey{}, new(atomic.Int64))
}

// shellSessionCounter returns the step's shell session counter, or nil
// when the call did not come through a step that set one (direct callers,
// tests). A nil counter disables spreading: calls fall back to the
// stable default, the pre-parallel behavior.
func shellSessionCounter(ctx context.Context) *atomic.Int64 {
	v, _ := ctx.Value(shellSessionCounterKey{}).(*atomic.Int64)
	return v
}
