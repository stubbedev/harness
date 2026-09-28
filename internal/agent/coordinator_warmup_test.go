package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

// warmupTestCoordinator builds a coordinator on a hermetic offline
// config: one openai-typed provider whose model resolves without
// network, selected as both large and small, so the builds behind
// Warmup run for real but never dial out. The background builds read
// the interactive flag (buildTools branches on it), so it has to be
// fixed at construction instead of mutated afterwards.
func warmupTestCoordinator(t *testing.T, interactive bool) Coordinator {
	t.Helper()

	env := testEnv(t)
	harnessJSON := `{
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true},
  "providers": {"mock": {"id": "mock", "name": "Mock", "type": "openai",
    "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
    "models": [{"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128}]}},
  "models": {"large": {"provider": "mock", "model": "mock-model"},
             "small": {"provider": "mock", "model": "mock-model"}}
}`
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "harness.yaml"), []byte(harnessJSON), 0o644))

	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	cfg.SetupAgents()

	coord, err := NewCoordinator(t.Context(), CoordinatorOptions{
		Config:      cfg,
		Sessions:    env.sessions,
		Messages:    env.messages,
		History:     env.history,
		FileTracker: *env.filetracker,
		Interactive: interactive,
	})
	require.NoError(t, err)
	return coord
}

// TestCoordinatorWarmupInstallsReadiness pins the warmup contract: the
// first Warmup completes run's prologue for real (readiness builds plus
// the first model/tool rebuild) and installs the memoized generation, so
// a submit that follows it finds everything hot; a second Warmup takes
// the memoized path and rebuilds nothing.
func TestCoordinatorWarmupInstallsReadiness(t *testing.T) {
	t.Parallel()

	coord := warmupTestCoordinator(t, true)
	c := coord.(*coordinator)
	require.False(t, c.rebuildGenValid, "precondition: a fresh coordinator has nothing warm")

	require.NoError(t, coord.Warmup(t.Context()))
	require.True(t, c.rebuildGenValid, "warmup installs the memoized model/tool generation")
	require.NotNil(t, c.currentAgent.Model().Model, "warmup leaves the coder model built")

	gen := c.rebuildGen
	require.NoError(t, coord.Warmup(t.Context()))
	require.Equal(t, gen, c.rebuildGen, "a second warmup takes the memoized path")
}

// TestCoordinatorWarmupNonInteractiveWaitsForInit pins the
// non-interactive branch: the warmup waits for MCP initialization
// through the same bounded seam run uses, so a headless run that starts
// after the warmup keeps its one-shot tool palette semantics.
func TestCoordinatorWarmupNonInteractiveWaitsForInit(t *testing.T) {
	t.Parallel()

	coord := warmupTestCoordinator(t, false)
	c := coord.(*coordinator)

	wants := 0
	c.waitForInit = func(context.Context) error {
		wants++
		return nil
	}

	require.NoError(t, coord.Warmup(t.Context()))
	require.Equal(t, 1, wants, "the non-interactive warmup waits for MCP init once")
	require.True(t, c.rebuildGenValid)
}
