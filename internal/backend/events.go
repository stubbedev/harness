package backend

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
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
