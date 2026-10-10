package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/home"
	"github.com/stubbedev/harness/internal/memory"
	"github.com/stubbedev/harness/internal/projects"
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
	importLegacyMemories(ctx, memStore)
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

// importLegacyMemories carries the memories of the per-workspace
// databases that predate the shared store into it, each once.
func importLegacyMemories(ctx context.Context, memStore *memory.Store) {
	projectDirs := map[string]string{}
	if list, err := projects.List(); err != nil {
		slog.Warn("Failed to read the project list for the memory import", "error", err)
	} else {
		// The list is most recently used first: a data directory that
		// served several working directories takes the latest.
		for _, p := range list {
			if _, ok := projectDirs[p.DataDir]; !ok && p.DataDir != "" {
				projectDirs[p.DataDir] = p.Path
			}
		}
	}
	report, err := memStore.ImportLegacy(ctx, memory.LegacySources(config.GlobalWorkspacesDir(), projectDirs))
	if err != nil {
		slog.Warn("Failed to import memories from workspace databases", "error", err)
		return
	}
	if report.Sources > 0 {
		slog.Info("Imported memories from workspace databases", "workspaces", report.Sources, "memories", report.Memories, "failed", report.Failed)
	}
}
