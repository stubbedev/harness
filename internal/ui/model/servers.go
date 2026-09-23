package model

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/stubbedev/harness/internal/ui/dialog"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

// openMCPServersDialog opens the MCP server manager over the memoized
// MCP states; state events keep it fresh while it stays open.
func (m *UI) openMCPServersDialog() tea.Cmd { //nolint:unparam // uniform openDialog dispatch signature
	if m.dialog.ContainsDialog(dialog.MCPServersID) {
		m.dialog.BringToFront(dialog.MCPServersID)
		return nil
	}
	m.dialog.OpenDialog(dialog.NewMCPServers(m.com, m.mcpStates))
	return nil
}

// openLSPServersDialog opens the LSP server manager over the memoized
// LSP states and diagnostic counts.
func (m *UI) openLSPServersDialog() tea.Cmd { //nolint:unparam // uniform openDialog dispatch signature
	if m.dialog.ContainsDialog(dialog.LSPServersID) {
		m.dialog.BringToFront(dialog.LSPServersID)
		return nil
	}
	m.dialog.OpenDialog(dialog.NewLSPServers(m.com, m.lspStates, m.lspDiagnostics))
	return nil
}

// runServerOp runs a workspace server operation off-thread and reports
// the outcome as a toast. The server manager dialogs stay open; state
// events refresh them as the operation takes effect.
func (m *UI) runServerOp(op func(ws workspace.Workspace) error, success string) tea.Cmd {
	ws := m.com.Workspace
	return func() tea.Msg {
		if err := op(ws); err != nil {
			return util.ReportError(err)()
		}
		return util.NewInfoMsg(success)
	}
}

// reconnectMCP restarts a named MCP server in the background.
func (m *UI) reconnectMCP(name string) tea.Cmd {
	return m.runServerOp(
		func(ws workspace.Workspace) error { return ws.MCPReconnect(context.Background(), name) },
		"Reconnecting MCP server "+name+"...",
	)
}

// disableMCPForSession turns a named MCP server off for the rest of the
// session without touching its configuration.
func (m *UI) disableMCPForSession(name string) tea.Cmd {
	return m.runServerOp(
		func(ws workspace.Workspace) error { return ws.MCPDisableForSession(context.Background(), name) },
		"Disabled MCP server "+name+" for this session",
	)
}

// restartLSP restarts a named running LSP server in the background.
func (m *UI) restartLSP(name string) tea.Cmd {
	return m.runServerOp(
		func(ws workspace.Workspace) error { return ws.LSPRestartSingle(context.Background(), name) },
		"Restarting LSP server "+name+"...",
	)
}

// setLSPSessionDisabled turns a named LSP server off (or back on) for
// the rest of the session. Re-enabling does not start the server; it
// starts on demand the next time a matching file is opened.
func (m *UI) setLSPSessionDisabled(name string, disabled bool) tea.Cmd {
	msg := "Enabled LSP server " + name + "; it starts on the next matching file"
	if disabled {
		msg = "Disabled LSP server " + name + " for this session"
	}
	return m.runServerOp(
		func(ws workspace.Workspace) error {
			return ws.LSPSetSessionDisabled(context.Background(), name, disabled)
		},
		msg,
	)
}
