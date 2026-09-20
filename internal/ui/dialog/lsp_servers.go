package dialog

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/workspace"
)

// LSPServersID is the identifier for the LSP servers dialog.
const LSPServersID = "lsp_servers"

// ActionLSPRestart restarts a named running LSP server.
type ActionLSPRestart struct {
	Name string
}

// ActionLSPSetSessionDisabled turns a named LSP server off (or back on)
// for the rest of the process without touching its configuration.
type ActionLSPSetSessionDisabled struct {
	Name     string
	Disabled bool
}

// LSPServers browses the LSP servers over the shared servers dialog
// machinery: a list with live status, and a detail view with facts and
// actions. Actions leave the dialog open; LSP state events refresh it
// through SetStates. See servers.go.
type LSPServers struct {
	serversDialog
	states      map[string]workspace.LSPClientInfo
	diagnostics map[string]lsp.DiagnosticCounts
}

var (
	_ Dialog        = (*LSPServers)(nil)
	_ serversSource = (*LSPServers)(nil)
)

// NewLSPServers creates the LSP server manager dialog over the given
// live states and diagnostic counts.
func NewLSPServers(com *common.Common, states map[string]workspace.LSPClientInfo, diagnostics map[string]lsp.DiagnosticCounts) *LSPServers {
	m := &LSPServers{states: states, diagnostics: diagnostics}
	km := dialogKeys()
	m.serversDialog = newServersDialog(com, m, km.LSPServers.Select, km.LSPServers.Back)
	return m
}

// SetStates refreshes the dialog with fresh LSP states and diagnostic
// counts. The current view survives the rebuild.
func (m *LSPServers) SetStates(states map[string]workspace.LSPClientInfo, diagnostics map[string]lsp.DiagnosticCounts) {
	m.states = states
	m.diagnostics = diagnostics
	m.refresh()
}

// -- serversSource --

func (m *LSPServers) dialogID() string { return LSPServersID }

func (m *LSPServers) title() string { return "LSP Servers" }

func (m *LSPServers) detailTitle(name string) string { return "LSP: " + name }

func (m *LSPServers) emptyText() string { return "No LSP servers running or configured." }

// lspConfigEntry returns the user's config entry for a server, if any.
func (m *LSPServers) lspConfigEntry(name string) (config.LSPConfig, bool) {
	if cfg := m.com.Config(); cfg != nil {
		entry, ok := cfg.LSP[name]
		return entry, ok
	}
	return config.LSPConfig{}, false
}

// entries is the union of the live states and the configured servers:
// running or tracked servers first-class, configured-but-never-started
// and config-disabled ones still visible.
func (m *LSPServers) entries() []string {
	names := make(map[string]struct{}, len(m.states))
	for name := range m.states {
		names[name] = struct{}{}
	}
	if cfg := m.com.Config(); cfg != nil {
		for name := range cfg.LSP {
			names[name] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(names))
}

// statusText is the config-aware status for a server: the configured
// wording for config-disabled servers, the live state wording
// otherwise, with how long ago a ready server came up.
func (m *LSPServers) statusText(name string) string {
	if cfgEntry, ok := m.lspConfigEntry(name); ok && cfgEntry.Disabled {
		return "disabled in configuration"
	}
	state, ok := m.states[name]
	if !ok {
		return "not started yet"
	}
	if state.State == lsp.StateReady && !state.ConnectedAt.IsZero() {
		return state.StatusText() + " · " + humanize.Time(state.ConnectedAt)
	}
	return state.StatusText()
}

func (m *LSPServers) detailItems(name string) []list.FilterableItem {
	cfgEntry, hasCfg := m.lspConfigEntry(name)
	state, hasState := m.states[name]

	b := newDetailBuilder(m.com, "lsp")
	b.Row("Status: " + m.statusText(name))
	if hasState && state.Error != nil {
		b.Row("Error: " + state.Error.Error())
	}
	if hasCfg {
		b.Row("Configured: user configuration")
		if cmd := strings.TrimSpace(strings.Join(append([]string{cfgEntry.Command}, cfgEntry.Args...), " ")); cmd != "" {
			b.Row("Command: " + cmd)
		}
		if len(cfgEntry.FileTypes) > 0 {
			b.Row("File types: " + strings.Join(cfgEntry.FileTypes, ", "))
		}
	} else {
		b.Row("Configured: auto-discovered")
	}
	if counts := lspDiagnosticsText(m.diagnostics[name]); counts != "" {
		b.Row("Diagnostics: " + counts)
	}

	switch {
	case hasCfg && cfgEntry.Disabled:
		return b.Row("Disabled in configuration").Items()
	case hasState && state.SessionDisabled:
		return b.
			Action("enable", "Enable for this session", "starts the next time a matching file is opened", ActionLSPSetSessionDisabled{Name: name, Disabled: false}).
			Items()
	default:
		if hasState {
			switch state.State {
			case lsp.StateReady, lsp.StateStarting, lsp.StateError:
				b.Action("restart", "Restart", "restart this server now", ActionLSPRestart{Name: name})
			}
		}
		return b.
			Action("disable", "Disable for this session", "stops it and blocks auto-start until re-enabled", ActionLSPSetSessionDisabled{Name: name, Disabled: true}).
			Items()
	}
}

// lspDiagnosticsText renders the non-zero diagnostic counts of a
// server.
func lspDiagnosticsText(counts lsp.DiagnosticCounts) string {
	var parts []string
	if counts.Error > 0 {
		parts = append(parts, fmt.Sprintf("%d errors", counts.Error))
	}
	if counts.Warning > 0 {
		parts = append(parts, fmt.Sprintf("%d warnings", counts.Warning))
	}
	if counts.Information > 0 {
		parts = append(parts, fmt.Sprintf("%d info", counts.Information))
	}
	if counts.Hint > 0 {
		parts = append(parts, fmt.Sprintf("%d hints", counts.Hint))
	}
	return strings.Join(parts, " · ")
}
