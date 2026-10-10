package lsp

import (
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/csync"
)

// NewTestClient returns a client with no language server behind it, for
// tests in other packages that need a manager holding diagnostics. It
// handles every file under cwd and holds whatever [Client.PublishTestDiagnostics]
// feeds it. Opening a file that does not exist fails before any server is
// reached, so a test that names only missing files never touches one; any
// other request to the server panics.
func NewTestClient(name, cwd string) *Client {
	c := &Client{
		name:        name,
		cwd:         cwd,
		diagnostics: csync.NewVersionedMap[protocol.DocumentURI, []protocol.Diagnostic](),
		openFiles:   csync.NewMap[string, *OpenFileInfo](),
	}
	c.SetServerState(StateReady)
	return c
}

// PublishTestDiagnostics records diagnostics for path as though the server
// had published them, replacing what it held for that file. An empty list
// clears the file, the way a server reports a fix.
func (c *Client) PublishTestDiagnostics(path string, diagnostics []protocol.Diagnostic) {
	c.diagnostics.Set(protocol.URIFromPath(path), diagnostics)
}

// HoldTestSettle marks a settle in flight, as a server still answering for a
// change would, until release is called. It lets a test in another package
// stand in for a server that never goes quiet.
func (s *Manager) HoldTestSettle() (release func()) {
	done := make(chan struct{})
	s.settleMu.Lock()
	s.settlePending = append(s.settlePending, done)
	s.settleMu.Unlock()
	return func() {
		s.settleMu.Lock()
		s.settlePending = deleteChan(s.settlePending, done)
		s.settleMu.Unlock()
		close(done)
	}
}
