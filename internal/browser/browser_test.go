package browser

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	pkgbrowser "github.com/pkg/browser"
	"github.com/stretchr/testify/require"
)

// TestOpenURLRunsTheOpenerSilently locks the invariant behind a clean
// sign-in prompt: whatever the opener prints goes nowhere instead of
// over the prompt or the TUI, whose screen it corrupted before.
func TestOpenURLRunsTheOpenerSilently(t *testing.T) {
	dir := t.TempDir()
	invoked := filepath.Join(dir, "invoked")
	script := "#!/bin/sh\n" +
		"printf '%s' \"$1\" > " + invoked + "\n" +
		"echo opener stdout is not the terminal\n" +
		"echo opener stderr is not the terminal >&2\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "xdg-open"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	require.Equal(t, io.Discard, pkgbrowser.Stdout, "the opener's stdout reaches the terminal")
	require.Equal(t, io.Discard, pkgbrowser.Stderr, "the opener's stderr reaches the terminal")

	require.NoError(t, OpenURL("https://opencode.ai/console/device"))

	opened, err := os.ReadFile(invoked)
	require.NoError(t, err, "the opener was never invoked")
	require.Equal(t, "https://opencode.ai/console/device", string(opened))
}
