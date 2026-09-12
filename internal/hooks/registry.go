package hooks

import (
	"context"

	"github.com/stubbedev/harness/internal/config"
)

// Registry fires hook events from anywhere in the agent pipeline. It reads
// the live config on every fire, so config reloads (including hook changes)
// take effect on the next event without rebuilding agents.
//
// A nil *Registry is valid and inert: every method treats it as "no hooks
// configured", so callers can hold an optional Registry without nil checks.
type Registry struct {
	cfg        *config.ConfigStore
	cwd        string
	projectDir string
}

// NewRegistry builds a Registry over the config store. cwd is the working
// directory hook commands run in; projectDir is exposed to them as
// HARNESS_PROJECT_DIR.
func NewRegistry(cfg *config.ConfigStore, cwd, projectDir string) *Registry {
	return &Registry{cfg: cfg, cwd: cwd, projectDir: projectDir}
}

// Has reports whether any hooks are configured for the event. Nil-safe.
func (r *Registry) Has(event string) bool {
	if r == nil || r.cfg == nil {
		return false
	}
	return len(r.cfg.Config().Hooks[event]) > 0
}

// Run fires the event through every configured hook. The context's Event
// field must be set; CWD defaults to the registry's working directory.
// Nil-safe: a nil registry reports "no hooks ran".
func (r *Registry) Run(ctx context.Context, ec EventContext) (AggregateResult, error) {
	if !r.Has(ec.Event) {
		return AggregateResult{Decision: DecisionNone}, nil
	}
	if ec.CWD == "" {
		ec.CWD = r.cwd
	}
	runner := NewRunner(r.cfg.Config().Hooks[ec.Event], r.cwd, r.projectDir)
	return runner.RunEvent(ctx, ec)
}
