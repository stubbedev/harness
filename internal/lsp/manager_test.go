package lsp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	powernapconfig "github.com/charmbracelet/x/powernap/pkg/config"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/lspcatalog"
)

func TestUnavailableBackoff(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	now := base

	manager := &Manager{
		unavailable: csync.NewMap[string, time.Time](),
		now:         func() time.Time { return now },
	}

	require.False(t, manager.recentlyUnavailable("gopls"))

	manager.markUnavailable("gopls")
	require.True(t, manager.recentlyUnavailable("gopls"))

	now = now.Add(unavailableRetryDelay + time.Second)
	require.False(t, manager.recentlyUnavailable("gopls"))
	_, exists := manager.unavailable.Get("gopls")
	require.False(t, exists)

	manager.markUnavailable("gopls")
	manager.clearUnavailable("gopls")
	require.False(t, manager.recentlyUnavailable("gopls"))
}

func TestCanAutoStartFiltersBeforeLookingUpCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		server  *powernapconfig.ServerConfig
		want    bool
		lookups int
	}{
		{
			name: "unhandled file type",
			server: &powernapconfig.ServerConfig{
				Command:   "typescript-language-server",
				FileTypes: []string{"typescript"},
			},
		},
		{
			name: "generic command",
			server: &powernapconfig.ServerConfig{
				Command:   "node",
				FileTypes: []string{"go"},
			},
		},
		{
			name: "handled file type",
			server: &powernapconfig.ServerConfig{
				Command:   "gopls",
				FileTypes: []string{"go"},
			},
			want:    true,
			lookups: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lookups := 0
			manager := &Manager{
				unavailable: csync.NewMap[string, time.Time](),
				now:         time.Now,
				lookPath: func(string) (string, error) {
					lookups++
					return "/usr/local/bin/gopls", nil
				},
			}

			got := manager.canAutoStart("test", "main.go", t.TempDir(), tt.server)

			require.Equal(t, tt.want, got)
			require.Equal(t, tt.lookups, lookups)
		})
	}
}

func TestCanAutoStartCachesMissingCommand(t *testing.T) {
	t.Parallel()

	lookups := 0
	manager := &Manager{
		unavailable: csync.NewMap[string, time.Time](),
		now:         time.Now,
		lookPath: func(string) (string, error) {
			lookups++
			return "", errors.New("not found")
		},
	}
	server := &powernapconfig.ServerConfig{
		Command:   "gopls",
		FileTypes: []string{"go"},
	}

	require.False(t, manager.canAutoStart("gopls", "main.go", t.TempDir(), server))
	require.False(t, manager.canAutoStart("gopls", "main.go", t.TempDir(), server))
	require.Equal(t, 1, lookups)
}

func TestStartAsyncFanoutIsDeduped(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	now := base

	manager := &Manager{
		recentFanout: csync.NewMap[string, time.Time](),
		now:          func() time.Time { return now },
	}

	require.True(t, manager.shouldFanout("/repo/internal/lsp/client.go"))
	// Same file type, same directory: the answer cannot have changed.
	require.False(t, manager.shouldFanout("/repo/internal/lsp/client.go"))
	require.False(t, manager.shouldFanout("/repo/internal/lsp/manager.go"))
	// A different file type, or the same one elsewhere, gets its own look:
	// which servers handle a file depends on both.
	require.True(t, manager.shouldFanout("/repo/internal/lsp/notes.md"))
	require.True(t, manager.shouldFanout("/repo/internal/agent/agent.go"))

	now = now.Add(fanoutRetryDelay + time.Second)
	require.True(t, manager.shouldFanout("/repo/internal/lsp/client.go"))
}

func TestStartAsyncFanoutWithoutCache(t *testing.T) {
	t.Parallel()

	// Managers built by hand (tests, zero values) have no cache and must
	// still fan out rather than silently never starting a server.
	manager := &Manager{now: time.Now}
	require.True(t, manager.shouldFanout("/repo/a.go"))
	require.True(t, manager.shouldFanout("/repo/a.go"))
}

func TestHandlesFiletypeMatchesFilesWithoutExtension(t *testing.T) {
	t.Parallel()

	fileTypes := []string{"justfile", "makefile", "containerfile", "sh"}
	for _, path := range []string{"/repo/justfile", "/repo/Justfile", "/repo/.justfile", "/repo/Makefile", "/repo/Containerfile", "/repo/run.sh"} {
		require.True(t, handlesFiletype("test", fileTypes, path), path)
	}
	for _, path := range []string{"/repo/notjustfile", "/repo/README", "/repo/run.bash"} {
		require.False(t, handlesFiletype("test", fileTypes, path), path)
	}
}

func newCatalogManager(t *testing.T, installed func(command string) bool) *Manager {
	t.Helper()
	manager := NewManager(config.NewTestStoreWithWorkingDir(&config.Config{}, t.TempDir()))
	manager.lookPath = func(command string) (string, error) {
		if installed(command) {
			return "/usr/bin/" + command, nil
		}
		return "", errors.New("not found")
	}
	return manager
}

// scriptWorkDir is a directory every shell, make, just and Docker server's
// root markers accept.
func scriptWorkDir(t *testing.T) string {
	t.Helper()
	workDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workDir, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "Dockerfile"), nil, 0o644))
	return workDir
}

var supersededScripts = map[string]string{
	"bashls":                 "run.sh",
	"shuck":                  "run.zsh",
	"just":                   "tasks.just",
	"dockerls":               "Dockerfile",
	"docker_language_server": "Dockerfile",
}

func TestCanAutoStartSkipsServersShellLSPSupersedes(t *testing.T) {
	t.Parallel()

	manager := newCatalogManager(t, func(string) bool { return true })
	workDir := scriptWorkDir(t)
	shellLSP, ok := manager.manager.GetServer(lspcatalog.ShellLSP)
	require.True(t, ok)
	for name, file := range supersededScripts {
		path := filepath.Join(workDir, file)
		server, ok := manager.manager.GetServer(name)
		require.True(t, ok, name)
		require.False(t, manager.canAutoStart(name, path, workDir, server), name)
		require.True(t, manager.canAutoStart(lspcatalog.ShellLSP, path, workDir, shellLSP), path)
	}
}

func TestCanAutoStartKeepsSupersededServersWithoutShellLSP(t *testing.T) {
	t.Parallel()

	manager := newCatalogManager(t, func(command string) bool { return command != "shell-lsp" })
	workDir := scriptWorkDir(t)
	for name, file := range supersededScripts {
		server, ok := manager.manager.GetServer(name)
		require.True(t, ok, name)
		require.True(t, manager.canAutoStart(name, filepath.Join(workDir, file), workDir, server), name)
	}
}

func TestStartAsyncFanoutTellsExtensionlessFilesApart(t *testing.T) {
	t.Parallel()

	manager := &Manager{
		recentFanout: csync.NewMap[string, time.Time](),
		now:          time.Now,
	}
	require.True(t, manager.shouldFanout("/repo/Makefile"))
	require.True(t, manager.shouldFanout("/repo/justfile"))
	require.True(t, manager.shouldFanout("/repo/Dockerfile"))
	require.False(t, manager.shouldFanout("/repo/justfile"))
}
