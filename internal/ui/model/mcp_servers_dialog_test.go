package model

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// mcpDialogUI builds a UI whose config carries the given MCP servers
// and whose memoized live states carry states, ready for the servers
// dialog to open.
func mcpDialogUI(t *testing.T, servers config.MCPs, states map[string]mcptools.ClientInfo) *UI {
	t.Helper()

	cfg := &config.Config{Options: &config.Options{}}
	cfg.MCP = servers
	ui := newFrameTestUI(t)
	ui.com.Workspace = &testWorkspace{cfg: cfg}
	ui.state = uiChat
	ui.session = &session.Session{ID: "s1"}
	ui.focus = uiFocusEditor
	ui.mcpStates = states
	return ui
}

// TestMCPServersDialogListsLiveServers pins what /mcp-servers must
// show: every configured server with its live status, so a connected
// server reads as connected the same way the ctrl+d details window
// reports it.
func TestMCPServersDialogListsLiveServers(t *testing.T) {
	ui := mcpDialogUI(t,
		config.MCPs{
			"notmuch": {Type: config.MCPStdio, Command: "notmuch-mcp"},
			"off":     {Type: config.MCPStdio, Command: "gone", Disabled: true},
		},
		map[string]mcptools.ClientInfo{
			"notmuch": {
				Name:        "notmuch",
				State:       mcptools.StateConnected,
				ConnectedAt: time.Now().Add(-time.Minute),
			},
		},
	)

	ui.openMCPServersDialog()
	dia := ui.dialog.Dialog(dialog.MCPServersID)
	require.NotNil(t, dia, "the palette command must open the servers dialog")

	screen := ansi.Strip(drawScreen(t, ui))
	require.Contains(t, screen, "MCP Servers")
	require.Contains(t, screen, "notmuch", "a configured server must be listed")
	require.Contains(t, screen, "connected", "its live status must be shown")
	require.Contains(t, screen, "off", "a disabled configured server is listed too")
}
