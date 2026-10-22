package lspcatalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewBundlesShellLSP(t *testing.T) {
	t.Parallel()

	server, ok := New().GetServer(ShellLSP)
	require.True(t, ok)
	require.Equal(t, "shell-lsp", server.Command)
	require.Contains(t, server.FileTypes, "justfile")
}

func TestSupersededServersAndSuccessorsAreInTheCatalog(t *testing.T) {
	t.Parallel()

	catalog := New()
	for name, successor := range supersededBy {
		_, ok := catalog.GetServer(name)
		require.True(t, ok, "superseded server %q is not bundled", name)
		_, ok = catalog.GetServer(successor)
		require.True(t, ok, "successor %q of %q is not bundled", successor, name)
	}
}

func TestNewReturnsIndependentCatalogs(t *testing.T) {
	t.Parallel()

	first := New()
	first.RemoveServer(ShellLSP)
	_, ok := New().GetServer(ShellLSP)
	require.True(t, ok)
}
