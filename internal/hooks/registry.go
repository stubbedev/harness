package hooks

import (
	"context"
	"log/slog"

	"github.com/stubbedev/harness/internal/config"
)

// Registry fires hook events from anywhere in the agent pipeline. It reads
// the live config on every fire, so config reloads (including hook changes)
// take effect on the next event without rebuilding agents.
//
// Alongside the shell commands in the config it can carry in-process
// dispatchers (Lua extensions), whose handlers answer the same events.
// Both sources are aggregated together: a deny from either denies.
//
// A nil *Registry is valid and inert: every method treats it as "no hooks
// configured", so callers can hold an optional Registry without nil checks.
type Registry struct {
	cfg         *config.ConfigStore
	cwd         string
	projectDir  string
	dispatchers []Dispatcher
}

// NewRegistry builds a Registry over the config store. cwd is the working
// directory hook commands run in; projectDir is exposed to them as
// HARNESS_PROJECT_DIR.
func NewRegistry(cfg *config.ConfigStore, cwd, projectDir string) *Registry {
	return &Registry{cfg: cfg, cwd: cwd, projectDir: projectDir}
}

// WithDispatchers returns the registry with the given in-process
// dispatchers attached. Nil dispatchers are ignored, so callers can pass
// an optional extension host straight through.
func (r *Registry) WithDispatchers(dispatchers ...Dispatcher) *Registry {
	if r == nil {
		return nil
	}
	for _, d := range dispatchers {
		if d == nil {
			continue
		}
		r.dispatchers = append(r.dispatchers, d)
	}
	return r
}

// Has reports whether any hooks are configured for the event, in the
// config or in an attached dispatcher. Nil-safe.
func (r *Registry) Has(event string) bool {
	if r == nil {
		return false
	}
	if r.cfg != nil && len(r.cfg.Config().Hooks[event]) > 0 {
		return true
	}
	for _, d := range r.dispatchers {
		if d.Has(event) {
			return true
		}
	}
	return false
}

// Run fires the event through every configured hook and every attached
// dispatcher. The context's Event field must be set; CWD defaults to the
// registry's working directory. Nil-safe: a nil registry reports "no
// hooks ran".
func (r *Registry) Run(ctx context.Context, ec EventContext) (AggregateResult, error) {
	if !r.Has(ec.Event) {
		return AggregateResult{Decision: DecisionNone}, nil
	}
	if ec.CWD == "" {
		ec.CWD = r.cwd
	}

	var (
		results []HookResult
		infos   []HookInfo
	)
	if r.cfg != nil && len(r.cfg.Config().Hooks[ec.Event]) > 0 {
		runner := NewRunner(r.cfg.Config().Hooks[ec.Event], r.cwd, r.projectDir)
		results, infos = runner.results(ctx, ec)
	}
	for _, d := range r.dispatchers {
		if !d.Has(ec.Event) {
			continue
		}
		for _, dispatched := range d.Dispatch(ctx, ec) {
			results = append(results, dispatched.Result)
			infos = append(infos, dispatched.info())
		}
	}

	if len(results) == 0 {
		return AggregateResult{Decision: DecisionNone}, nil
	}

	agg := aggregate(results, ec.ToolInput)
	agg.Hooks = infos
	slog.Info(
		"Hook completed",
		"event", ec.Event,
		"tool", ec.ToolName,
		"hooks", len(results),
		"decision", agg.Decision.String(),
	)
	return agg, nil
}
