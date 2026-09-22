package dialog

import (
	"errors"
	"image"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/workspace"
)

// serversWorkspace is the minimal workspace the server manager dialogs
// need: a config with MCP and LSP entries.
type serversWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *serversWorkspace) Config() *config.Config { return w.cfg }

// WorkingDir gives the embedded workspace interface a value without a
// real workspace; dialogs that only need the path (e.g. the file
// picker) work against the test's own directory.
func (w *serversWorkspace) WorkingDir() string { return "." }

// newServersTestCommon builds a Common over a config with three MCP
// servers (stdio, remote, config-disabled) and two LSP entries.
func newServersTestCommon() *common.Common {
	st := styles.CharmtonePantera()
	cfg := &config.Config{
		MCP: config.MCPs{
			"alpha": {Type: config.MCPStdio, Command: "npx", Args: []string{"-y", "alpha"}, Timeout: 10},
			"beta":  {Type: config.MCPHttp, URL: "http://localhost:3000/mcp"},
			"gamma": {Type: config.MCPStdio, Command: "gamma", Disabled: true},
		},
		LSP: config.LSPs{
			"gopls": {Command: "gopls", FileTypes: []string{"go"}},
			"pylsp": {Command: "pylsp", Disabled: true},
		},
	}
	return &common.Common{Styles: &st, Workspace: &serversWorkspace{cfg: cfg}}
}

// newMentionTestCommon builds a Common over an empty config; dialogs
// that need no workspace state (e.g. the mention picker) use it.
func newMentionTestCommon() *common.Common {
	st := styles.CharmtonePantera()
	return &common.Common{Styles: &st, Workspace: &serversWorkspace{cfg: &config.Config{}}}
}

// drawServers draws a dialog once at a typical terminal size, so any
// panic on the render path fails the test.
func drawServers(t *testing.T, d Dialog) {
	t.Helper()
	scr := uv.NewScreenBuffer(120, 40)
	d.Draw(scr, uv.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(120, 40)})
}

// TestMCPServersConstructAndBrowse pins the construction contract of
// the MCP server manager: building it must not read through the
// not-yet-embedded serversDialog. newServersDialog used to refresh
// during construction, which dereferenced a nil Common through the
// zero-value embedding, so every attempt to open the dialog crashed
// the TUI.
func TestMCPServersConstructAndBrowse(t *testing.T) {
	t.Parallel()

	d := NewMCPServers(newServersTestCommon(), map[string]mcp.ClientInfo{
		"alpha": {Name: "alpha", State: mcp.StateConnected, ConnectedAt: time.Now().Add(-time.Minute), Counts: mcp.Counts{Tools: 3}},
		"beta":  {Name: "beta", State: mcp.StateError, Error: errors.New("boom")},
		"delta": {Name: "delta", State: mcp.StateNeedsAuth},
	})

	require.Equal(t, []string{"alpha", "beta", "gamma"}, d.listNames,
		"the constructor must populate the server list itself")

	drawServers(t, d)

	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, serversPhaseDetail, d.phase)
	require.Equal(t, "alpha", d.current)
	drawServers(t, d)

	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Equal(t, serversPhaseList, d.phase)
	drawServers(t, d)

	d.SetStates(map[string]mcp.ClientInfo{})
	drawServers(t, d)
}

// TestServersDialogFilterInput pins the filter input every dialog now
// carries: typing filters the list, and enter resolves the selection
// through the selected item - not its position - so opening the detail
// still lands on the server the user sees after filtering.
func TestServersDialogFilterInput(t *testing.T) {
	t.Parallel()

	d := NewMCPServers(newServersTestCommon(), map[string]mcp.ClientInfo{
		"alpha": {Name: "alpha", State: mcp.StateConnected},
		"beta":  {Name: "beta", State: mcp.StateError, Error: errors.New("boom")},
	})

	// Type a filter that only beta matches.
	for _, r := range "beta" {
		d.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	require.Equal(t, "beta", d.input.Value())
	require.Equal(t, 1, len(d.list.FilteredItems()), "the filter narrows the list")

	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, serversPhaseDetail, d.phase)
	require.Equal(t, "beta", d.current, "selection resolves through the item, not the pre-filter index")
}

// TestLSPServersConstructAndBrowse pins the same construction contract
// for the LSP server manager, over the union of live states and config
// entries.
func TestLSPServersConstructAndBrowse(t *testing.T) {
	t.Parallel()

	d := NewLSPServers(
		newServersTestCommon(),
		map[string]workspace.LSPClientInfo{
			"gopls":         {Name: "gopls", State: lsp.StateReady, ConnectedAt: time.Now().Add(-time.Minute)},
			"rust-analyzer": {Name: "rust-analyzer", State: lsp.StateError, Error: errors.New("no such binary")},
		},
		map[string]lsp.DiagnosticCounts{"gopls": {Error: 1, Warning: 2}},
	)

	require.Equal(t, []string{"gopls", "pylsp", "rust-analyzer"}, d.listNames)

	drawServers(t, d)

	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, serversPhaseDetail, d.phase)
	require.Equal(t, "gopls", d.current)
	drawServers(t, d)

	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Equal(t, serversPhaseList, d.phase)
	drawServers(t, d)
}
