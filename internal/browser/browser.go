// Package browser opens URLs in the user's browser without letting the
// opener's output reach the terminal. The opener (xdg-open and the
// browser it starts) inherits nothing: its chatter would otherwise
// interleave with a sign-in prompt, and corrupt the TUI's screen.
package browser

import (
	"io"

	pkgbrowser "github.com/pkg/browser"
)

func init() {
	pkgbrowser.Stdout = io.Discard
	pkgbrowser.Stderr = io.Discard
}

// OpenURL opens url in the user's browser.
func OpenURL(url string) error {
	return pkgbrowser.OpenURL(url)
}
