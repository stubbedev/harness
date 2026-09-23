package dialog

import (
	"fmt"
	"strings"

	"github.com/dustin/go-humanize"
	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
)

// MCPServersID is the identifier for the MCP servers dialog.
const MCPServersID ID = "mcp_servers"

// ActionMCPReconnect restarts a named MCP server, clearing a
// session-scoped disable.
type ActionMCPReconnect struct {
	Name string
}

// ActionMCPDisableForSession disables a named MCP server for the rest
// of the process without touching its configuration.
type ActionMCPDisableForSession struct {
	Name string
}

// ActionOpenMCPAuth asks the model to open the MCP authentication
// dialog on top of this one.
type ActionOpenMCPAuth struct{}

// MCPServers browses the configured MCP servers over the shared
// servers dialog machinery: a list with live status, and a detail view
// with facts and actions. Actions leave the dialog open; MCP state
// events refresh it through SetStates. See servers.go.
type MCPServers struct {
	serversDialog
	states map[string]mcptools.ClientInfo
}

var (
	_ Dialog        = (*MCPServers)(nil)
	_ serversSource = (*MCPServers)(nil)
)

// NewMCPServers creates the MCP server manager dialog over the given
// live client states.
func NewMCPServers(com *common.Common, states map[string]mcptools.ClientInfo) *MCPServers {
	m := &MCPServers{states: states}
	km := dialogKeys()
	m.serversDialog = newServersDialog(com, m, km.MCPServers.Select, km.MCPServers.Back)
	m.refresh()
	return m
}

// SetStates refreshes the dialog with fresh MCP client states. The
// current view survives the rebuild.
func (m *MCPServers) SetStates(states map[string]mcptools.ClientInfo) {
	m.states = states
	m.refresh()
}

// -- serversSource --

func (m *MCPServers) dialogID() ID { return MCPServersID }

func (m *MCPServers) title() string { return "MCP Servers" }

func (m *MCPServers) detailTitle(name string) string { return "MCP: " + name }

func (m *MCPServers) emptyText() string { return "No MCP servers configured." }

func (m *MCPServers) entries() []string {
	sorted := m.com.Config().MCP.Sorted()
	names := make([]string, len(sorted))
	for i, entry := range sorted {
		names[i] = entry.Name
	}
	return names
}

// statusText is the config-aware status for a server: the configured
// wording for config-disabled servers, the live state wording
// otherwise, with how long ago a connected server came up.
func (m *MCPServers) statusText(name string) string {
	if entry := m.com.Config().MCP[name]; entry.Disabled {
		return "disabled in configuration"
	}
	state, ok := m.states[name]
	if !ok {
		return "starting…"
	}
	if state.State == mcptools.StateConnected && !state.ConnectedAt.IsZero() {
		return state.StatusText() + " · " + humanize.Time(state.ConnectedAt)
	}
	return state.StatusText()
}

func (m *MCPServers) detailItems(name string) []list.FilterableItem {
	entry := m.com.Config().MCP[name]
	state, hasState := m.states[name]
	sessionDisabled := hasState && state.State == mcptools.StateDisabled && !entry.Disabled

	b := newDetailBuilder(m.com, "mcp")
	b.Row("Status: " + m.statusText(name))
	if hasState && state.Error != nil {
		b.Row("Error: " + state.Error.Error())
	}
	b.Row("Type: " + string(entry.Type))
	switch entry.Type {
	case config.MCPStdio:
		if cmd := strings.TrimSpace(strings.Join(append([]string{entry.Command}, entry.Args...), " ")); cmd != "" {
			b.Row("Command: " + cmd)
		}
	default:
		if entry.URL != "" {
			b.Row("URL: " + entry.URL)
		}
	}
	if hasState {
		if counts := state.Counts.String(); counts != "" {
			b.Row("Capabilities: " + counts)
		}
	}
	if entry.Timeout > 0 {
		b.Row(fmt.Sprintf("Timeout: %ds", entry.Timeout))
	}

	if entry.Disabled {
		return b.Row("Disabled in configuration").Items()
	}
	if sessionDisabled {
		return b.
			Action("reconnect", "Reconnect", "clears the session disable and restarts now", ActionMCPReconnect{Name: name}).
			Items()
	}
	b.Action("reconnect", "Reconnect", "restart this server now", ActionMCPReconnect{Name: name})
	b.Action("disable", "Disable for this session", "stays off until reconnected or restarted", ActionMCPDisableForSession{Name: name})
	if hasState && state.State == mcptools.StateNeedsAuth {
		b.Action("authenticate", "Authenticate", "open the authorization flow", ActionOpenMCPAuth{})
	}
	return b.Items()
}
