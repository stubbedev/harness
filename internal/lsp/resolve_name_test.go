package lsp

import (
	"testing"

	powernapconfig "github.com/charmbracelet/x/powernap/pkg/config"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
)

// A config entry under a name of the user's own, whose command is a
// bundled server's binary by absolute path, configures that bundled
// server rather than adding a second one beside it.
func TestNewManagerMergesEntryByCommandBinary(t *testing.T) {
	t.Parallel()

	store := config.NewTestStoreWithWorkingDir(&config.Config{
		LSP: config.LSPs{
			"phpantom": {Command: "/nix/store/abc-phpantom_lsp-0.11.1/bin/phpantom_lsp", FileTypes: []string{"php"}},
		},
	}, t.TempDir())
	m := NewManager(store)

	servers := m.manager.GetServers()
	require.NotContains(t, servers, "phpantom", "the entry ran as a server of its own")
	require.Contains(t, servers, "phpantom_lsp")
	require.Equal(t, "/nix/store/abc-phpantom_lsp-0.11.1/bin/phpantom_lsp", servers["phpantom_lsp"].Command)
	require.True(t, m.isUserConfigured("phpantom_lsp"), "the merged server lost its user config")
}

func TestResolveServerName(t *testing.T) {
	t.Parallel()

	manager := powernapconfig.NewManager()
	manager.AddServer("alpha", &powernapconfig.ServerConfig{Command: "alpha-ls"})
	manager.AddServer("beta", &powernapconfig.ServerConfig{Command: "shared-ls"})
	manager.AddServer("gamma", &powernapconfig.ServerConfig{Command: "shared-ls"})

	require.Equal(t, "alpha", resolveServerName(manager, "alpha", ""), "an exact name wins")
	require.Equal(t, "alpha", resolveServerName(manager, "alpha-ls", ""), "a name that is a command")
	require.Equal(t, "alpha", resolveServerName(manager, "mine", "/opt/bin/alpha-ls"), "a command by absolute path")
	require.Equal(t, "mine", resolveServerName(manager, "mine", "/opt/bin/shared-ls"), "an ambiguous command resolves to none")
	require.Equal(t, "mine", resolveServerName(manager, "mine", "/opt/bin/other"), "an unknown server is the user's own")
}

// Disabling an entry named after a bundled server's command disables
// that server.
func TestNewManagerDisablesByCommandName(t *testing.T) {
	t.Parallel()

	store := config.NewTestStoreWithWorkingDir(&config.Config{
		LSP: config.LSPs{"phpantom_lsp": {Disabled: true}},
	}, t.TempDir())
	m := NewManager(store)
	require.NotContains(t, m.manager.GetServers(), "phpantom_lsp")
}
