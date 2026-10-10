package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/home"
	"github.com/stubbedev/harness/internal/memory"
)

// startupDataRoot is the global data root as the process started with
// it, before any test redirected it.
var startupDataRoot = home.DataDir()

// openMemory opens the machine-wide memory store and returns the
// service of the workspace store belongs to, with the release of the
// store for the app's cleanup. A store that cannot be opened turns
// memory off for this workspace, logged, rather than failing startup.
func openMemory(ctx context.Context, store *config.ConfigStore) (memory.Service, func(context.Context) error) {
	noop := func(context.Context) error { return nil }
	// A test that never redirected the data root would open the
	// memories of whoever runs it.
	if testing.Testing() && home.DataDir() == startupDataRoot {
		return nil, noop
	}

	memStore, err := memory.OpenStore(ctx, config.GlobalMemoryDir())
	if err != nil {
		slog.Warn("Failed to open the memory store; memory is off for this workspace", "error", err)
		return nil, noop
	}
	repoKey := memory.RepoKey(ctx, store.WorkingDir())
	slog.Debug("Opened the memory store", "repo_key", repoKey)

	// The reap limit is read lazily so live config reloads of
	// options.memory.max_memories are honored without rebuilding.
	svc := memory.NewService(memStore, repoKey, memory.WithReapLimit(func() int {
		if cfg := store.Config(); cfg != nil && cfg.Options != nil {
			return cfg.Options.Memory.GetMaxMemories()
		}
		return config.DefaultMaxMemories
	}))
	return svc, func(context.Context) error { return memStore.Close() }
}
