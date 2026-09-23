package backend

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"

	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/app"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/pubsub"
)

// SubscribeEvents returns a per-caller event channel for a workspace.
// Each caller receives all events; multiple callers do not compete.
func (b *Backend) SubscribeEvents(ctx context.Context, workspaceID string) (<-chan pubsub.Event[tea.Msg], error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Events(ctx), nil
}

// GetLSPStates returns the state of all LSP clients.
func (b *Backend) GetLSPStates(workspaceID string) (map[string]app.LSPClientInfo, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	return app.LSPStatesWithSessionDisabled(ws.LSPManager), nil
}

// GetLSPDiagnostics returns diagnostics for a specific LSP client in
// the workspace.
func (b *Backend) GetLSPDiagnostics(workspaceID, lspName string) (map[protocol.DocumentURI][]protocol.Diagnostic, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	for name, client := range ws.LSPManager.Clients().Seq2() {
		if name == lspName {
			return client.GetDiagnostics(), nil
		}
	}

	return nil, ErrLSPClientNotFound
}

// GetWorkspaceConfig returns the workspace-level configuration.
func (b *Backend) GetWorkspaceConfig(workspaceID string) (*config.Config, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Cfg.Config(), nil
}

// GetWorkspaceProviders returns the configured providers for a
// workspace.
func (b *Backend) GetWorkspaceProviders(workspaceID string) (any, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	providers, _ := config.Providers(ws.Cfg.Config())
	return providers, nil
}

// LSPStart starts an LSP server for the given path.
func (b *Backend) LSPStart(ctx context.Context, workspaceID, path string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	ws.LSPManager.Start(ctx, path)
	return nil
}

// LSPStopAll stops all LSP servers for a workspace.
func (b *Backend) LSPStopAll(ctx context.Context, workspaceID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	ws.LSPManager.StopAll(ctx)
	return nil
}

// LSPRestartSingle restarts a named running LSP server.
func (b *Backend) LSPRestartSingle(workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	return ws.LSPManager.RestartSingle(name)
}

// LSPSetSessionDisabled turns a named LSP server off (or back on) for
// the rest of the process without touching its configuration.
func (b *Backend) LSPSetSessionDisabled(ctx context.Context, workspaceID, name string, disabled bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	ws.LSPManager.SetSessionDisabled(ctx, name, disabled)
	return nil
}

// MCPGetStates returns the current state of all MCP clients. MCP state
// is process-wide, but the workspace is still resolved so a client
// polling with a stale ID gets the 404 that triggers its recovery.
func (b *Backend) MCPGetStates(workspaceID string) (map[string]mcptools.ClientInfo, error) {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return nil, err
	}
	return mcptools.GetStates(), nil
}

// MCPRefreshPrompts refreshes prompts for a named MCP client.
func (b *Backend) MCPRefreshPrompts(ctx context.Context, workspaceID, name string) error {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return err
	}
	mcptools.RefreshPrompts(ctx, name)
	return nil
}

// MCPRefreshResources refreshes resources for a named MCP client.
func (b *Backend) MCPRefreshResources(ctx context.Context, workspaceID, name string) error {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return err
	}
	mcptools.RefreshResources(ctx, name)
	return nil
}

// MCPPendingAuth returns the MCP servers awaiting OAuth authentication,
// for clients that need to prompt the user. workspaceID selects the
// workspace whose config provides the server URLs.
func (b *Backend) MCPPendingAuth(workspaceID string) ([]mcptools.PendingAuthServer, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	return mcptools.PendingAuthMCPs(ws.Cfg), nil
}

// MCPAuthURL returns the current OAuth authorization URL for a named
// server, if a flow is in progress.
func (b *Backend) MCPAuthURL(workspaceID, name string) (string, error) {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return "", err
	}
	return mcptools.MCPAuthURL(name), nil
}

// MCPAuthenticate runs the OAuth flow for a named MCP server with the
// local browser suppressed: the authorization URL is exposed via
// MCPAuthURL/MCPPendingAuth for the calling client to open on the user's
// machine. The call blocks until the flow completes, fails, or ctx is
// cancelled. workspaceID selects the workspace whose config drives the
// flow.
func (b *Backend) MCPAuthenticate(ctx context.Context, workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	finish, cancel, err := mcptools.BeginAuth(ws.Cfg, name)
	if err != nil {
		return err
	}
	defer cancel()
	return finish(ctx)
}

// MCPReconnect restarts a named MCP server, clearing a session-scoped
// disable.
func (b *Backend) MCPReconnect(ctx context.Context, workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	return mcptools.ReconnectSingle(ctx, ws.Cfg, name)
}

// MCPDisableForSession disables a named MCP server for the rest of the
// process without touching its configuration.
func (b *Backend) MCPDisableForSession(workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	return mcptools.DisableSingleForSession(ws.Cfg, name)
}
