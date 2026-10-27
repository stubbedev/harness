//go:build darwin || linux

package browser

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// openerCommand is the command pkg/browser runs a URL through on this
// platform, and the one the fake below has to stand in for.
func openerCommand() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

// TestOpenURLRunsTheOpenerSilently proves the URL reaches the opener
// while its output stays away from the terminal.
func TestOpenURLRunsTheOpenerSilently(t *testing.T) {
	dir := t.TempDir()
	invoked := filepath.Join(dir, "invoked")
	script := "#!/bin/sh\n" +
		"printf '%s' \"$1\" > " + invoked + "\n" +
		"echo opener stdout is not the terminal\n" +
		"echo opener stderr is not the terminal >&2\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, openerCommand()), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	require.NoError(t, OpenURL("https://opencode.ai/console/device"))

	opened, err := os.ReadFile(invoked)
	require.NoError(t, err, "the opener was never invoked")
	require.Equal(t, "https://opencode.ai/console/device", string(opened))
}
