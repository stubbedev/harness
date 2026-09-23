package lsp

import (
	"context"
	"log/slog"
	"os"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

// closeVanishedFiles forgets tracked open files whose path no longer exists
// on disk. A file renamed or deleted underneath the session would otherwise
// stay open forever: it can never be re-read from disk, so it survives every
// refresh and every restart, and its last published diagnostics are reported
// as though still current. The server is told the document closed before the
// entries are dropped, so it stops analyzing a buffer with no file behind it.
func (c *Client) closeVanishedFiles(ctx context.Context) {
	if c == nil {
		return
	}
	var vanished []string
	for uri := range c.openFiles.Seq2() {
		path, err := protocol.DocumentURI(uri).Path()
		if err != nil {
			continue
		}
		if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
			continue
		}
		vanished = append(vanished, uri)
	}
	for _, uri := range vanished {
		if c.pn() != nil {
			if err := c.pn().NotifyDidCloseTextDocument(ctx, uri); err != nil {
				slog.Debug("Failed to close vanished file with server", "uri", uri, "error", err)
			}
		}
		c.openFiles.Del(uri)
		c.diagnostics.Del(protocol.DocumentURI(uri))
	}
}
