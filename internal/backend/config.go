package backend

import (
	"context"

	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/crash"
	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/workspace"
)

// publishConfigChanged publishes a ConfigChanged event on the workspace's
// event broker so all subscribers (e.g. remote clients) refresh their
// cached config snapshot. It also re-initializes any MCP servers whose
// configuration changed as a result of the write.
func publishConfigChanged(ws *Workspace) {
	if ws == nil || ws.App == nil {
		return
	}

	// Re-init MCP servers whose config changed. MCP state is process-global,
	// so this only needs to happen once regardless of which workspace
	// triggered the write. Run async so unrelated config writes (model
	// switches, API keys) don't block on MCP reconciliation. Bound to the
	// workspace ctx so teardown cancels any in-flight init.
	crash.Go("mcp.Reinitialize", func() { mcptools.Reinitialize(ws.ctx, ws.Cfg) })

	ws.SendEvent(pubsub.Event[proto.ConfigChanged]{
		Type:    pubsub.UpdatedEvent,
		Payload: proto.ConfigChanged{WorkspaceID: ws.ID},
	})
}

// mutateConfig runs a config write against the workspace and, when it
// succeeds, tells every subscriber to refresh its config snapshot.
func (b *Backend) mutateConfig(workspaceID string, write func(*workspace.AppWorkspace) error) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := write(ws.Ops()); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// SetConfigField sets a key/value pair in the config file for the
// given scope.
func (b *Backend) SetConfigField(workspaceID string, scope config.Scope, key string, value any) error {
	return b.mutateConfig(workspaceID, func(ops *workspace.AppWorkspace) error {
		return ops.SetConfigField(scope, key, value)
	})
}

// RemoveConfigField removes a key from the config file for the given
// scope.
func (b *Backend) RemoveConfigField(workspaceID string, scope config.Scope, key string) error {
	return b.mutateConfig(workspaceID, func(ops *workspace.AppWorkspace) error {
		return ops.RemoveConfigField(scope, key)
	})
}

// UpdatePreferredModel updates the preferred model for the given type
// and persists it to the config file at the given scope.
func (b *Backend) UpdatePreferredModel(workspaceID string, scope config.Scope, modelType config.SelectedModelType, model config.SelectedModel) error {
	return b.mutateConfig(workspaceID, func(ops *workspace.AppWorkspace) error {
		return ops.UpdatePreferredModel(scope, modelType, model)
	})
}

// SetCompactMode sets the compact mode setting and persists it.
func (b *Backend) SetCompactMode(workspaceID string, scope config.Scope, enabled bool) error {
	return b.mutateConfig(workspaceID, func(ops *workspace.AppWorkspace) error {
		return ops.SetCompactMode(scope, enabled)
	})
}

// SetProviderAPIKey sets the API key for a provider and persists it.
func (b *Backend) SetProviderAPIKey(workspaceID string, scope config.Scope, providerID string, apiKey any) error {
	return b.mutateConfig(workspaceID, func(ops *workspace.AppWorkspace) error {
		return ops.SetProviderAPIKey(scope, providerID, apiKey)
	})
}

// RefreshOAuthToken refreshes the OAuth token for a provider.
func (b *Backend) RefreshOAuthToken(ctx context.Context, workspaceID string, scope config.Scope, providerID string) error {
	return b.mutateConfig(workspaceID, func(ops *workspace.AppWorkspace) error {
		return ops.RefreshOAuthToken(ctx, scope, providerID)
	})
}

// ImportCopilot attempts to import a GitHub Copilot token from disk.
func (b *Backend) ImportCopilot(workspaceID string) (*oauth.Token, bool, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, false, err
	}
	token, ok := ws.Ops().ImportCopilot()
	if ok {
		publishConfigChanged(ws)
	}
	return token, ok, nil
}
