package browser

import (
	"io"
	"testing"

	pkgbrowser "github.com/pkg/browser"
	"github.com/stretchr/testify/require"
)

// TestOpenerOutputIsDiscarded locks the invariant behind a clean sign-in
// prompt: whatever the opener prints goes nowhere instead of over the
// prompt or the TUI, whose screen it corrupted before.
func TestOpenerOutputIsDiscarded(t *testing.T) {
	require.Equal(t, io.Discard, pkgbrowser.Stdout, "the opener's stdout reaches the terminal")
	require.Equal(t, io.Discard, pkgbrowser.Stderr, "the opener's stderr reaches the terminal")
}
