package lsp

import (
	"errors"
	"testing"
	"time"

	powernapconfig "github.com/charmbracelet/x/powernap/pkg/config"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/csync"
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
