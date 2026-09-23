package backend

import (
	"context"

	"github.com/stubbedev/harness/internal/agent"
	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/crash"
	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/skills"
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

// MCPResourceContents holds the contents of an MCP resource returned
// by the backend.
type MCPResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     []byte `json:"blob,omitempty"`
}

// SetConfigField sets a key/value pair in the config file for the
// given scope.
func (b *Backend) SetConfigField(workspaceID string, scope config.Scope, key string, value any) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.SetConfigField(scope, key, value); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// RemoveConfigField removes a key from the config file for the given
// scope.
func (b *Backend) RemoveConfigField(workspaceID string, scope config.Scope, key string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.RemoveConfigField(scope, key); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// UpdatePreferredModel updates the preferred model for the given type
// and persists it to the config file at the given scope.
func (b *Backend) UpdatePreferredModel(workspaceID string, scope config.Scope, modelType config.SelectedModelType, model config.SelectedModel) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.UpdatePreferredModel(scope, modelType, model); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// SetCompactMode sets the compact mode setting and persists it.
func (b *Backend) SetCompactMode(workspaceID string, scope config.Scope, enabled bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.SetCompactMode(scope, enabled); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// SetProviderAPIKey sets the API key for a provider and persists it.
func (b *Backend) SetProviderAPIKey(workspaceID string, scope config.Scope, providerID string, apiKey any) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.SetProviderAPIKey(scope, providerID, apiKey); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// ImportCopilot attempts to import a GitHub Copilot token from disk.
func (b *Backend) ImportCopilot(workspaceID string) (*oauth.Token, bool, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, false, err
	}
	token, ok := ws.Cfg.ImportCopilot()
	if ok {
		publishConfigChanged(ws)
	}
	return token, ok, nil
}

// RefreshOAuthToken refreshes the OAuth token for a provider.
func (b *Backend) RefreshOAuthToken(ctx context.Context, workspaceID string, scope config.Scope, providerID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.RefreshOAuthToken(ctx, scope, providerID); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// InitializePrompt builds the initialization prompt for the workspace.
func (b *Backend) InitializePrompt(workspaceID string) (string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return "", err
	}
	return agent.InitializePrompt(ws.Cfg)
}

// ReadSkill reads a skill's content by ID.
func (b *Backend) ReadSkill(ctx context.Context, workspaceID, skillID string) ([]byte, proto.SkillReadResult, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, proto.SkillReadResult{}, err
	}

	mgr := ws.Skills
	content, result, err := skills.ReadContent(
		mgr.ActiveSkills(), mgr.ResolvedPaths(), mgr.WorkingDir(), skillID,
	)
	if err != nil {
		return nil, proto.SkillReadResult{}, err
	}
	return content, proto.SkillReadResult{
		Name:        result.Name,
		Description: result.Description,
		Source:      string(result.Source),
		Builtin:     result.Builtin,
	}, nil
}

// ListSkills returns the effective visible skills for a workspace.
func (b *Backend) ListSkills(workspaceID string) ([]proto.SkillInfo, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	mgr := ws.Skills
	entries := skills.Catalog(mgr.ActiveSkills(), mgr.ResolvedPaths(), mgr.WorkingDir())
	result := make([]proto.SkillInfo, len(entries))
	for i, entry := range entries {
		result[i] = proto.SkillInfo{
			ID:            entry.ID,
			Name:          entry.Name,
			Description:   entry.Description,
			Label:         entry.Label,
			Source:        string(entry.Source),
			UserInvocable: entry.UserInvocable,
		}
	}
	return result, nil
}

// RefreshMCPTools refreshes the tools for a named MCP server.
func (b *Backend) RefreshMCPTools(ctx context.Context, workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	mcptools.RefreshTools(ctx, ws.Cfg, name)
	return nil
}

// ReadMCPResource reads a resource from a named MCP server.
func (b *Backend) ReadMCPResource(ctx context.Context, workspaceID, name, uri string) ([]MCPResourceContents, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	contents, err := mcptools.ReadResource(ctx, ws.Cfg, name, uri)
	if err != nil {
		return nil, err
	}
	result := make([]MCPResourceContents, len(contents))
	for i, c := range contents {
		result[i] = MCPResourceContents{
			URI:      c.URI,
			MIMEType: c.MIMEType,
			Text:     c.Text,
			Blob:     c.Blob,
		}
	}
	return result, nil
}

// GetMCPPrompt retrieves a prompt from a named MCP server.
func (b *Backend) GetMCPPrompt(workspaceID, clientID, promptID string, args map[string]string) (string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return "", err
	}
	return commands.GetMCPPrompt(ws.Cfg, clientID, promptID, args)
}

func (b *Backend) ListMCPPrompts(workspaceID string) ([]proto.MCPPrompt, error) {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return nil, err
	}
	prompts := commands.LoadMCPPrompts()
	result := make([]proto.MCPPrompt, len(prompts))
	for i, prompt := range prompts {
		arguments := make([]proto.MCPPromptArgument, len(prompt.Arguments))
		for j, argument := range prompt.Arguments {
			arguments[j] = proto.MCPPromptArgument{
				ID:          argument.ID,
				Title:       argument.Title,
				Description: argument.Description,
				Required:    argument.Required,
			}
		}
		result[i] = proto.MCPPrompt{
			ID:          prompt.ID,
			Title:       prompt.Title,
			Description: prompt.Description,
			PromptID:    prompt.PromptID,
			ClientID:    prompt.ClientID,
			Arguments:   arguments,
		}
	}
	return result, nil
}

// GetWorkingDir returns the working directory for a workspace.
func (b *Backend) GetWorkingDir(workspaceID string) (string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return "", err
	}
	return ws.Cfg.WorkingDir(), nil
}

// ListExtensionCommands returns the slash commands the workspace's Lua
// extensions registered.
func (b *Backend) ListExtensionCommands(workspaceID string) ([]proto.ExtensionCommandInfo, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	commands := ws.Extensions.Commands()
	result := make([]proto.ExtensionCommandInfo, len(commands))
	for i, cmd := range commands {
		args := make([]proto.ExtensionCommandArgument, len(cmd.Arguments))
		for j, arg := range cmd.Arguments {
			args[j] = proto.ExtensionCommandArgument{
				ID:          arg.ID,
				Title:       arg.Title,
				Description: arg.Description,
				Required:    arg.Required,
			}
		}
		result[i] = proto.ExtensionCommandInfo{
			ID:          cmd.ID,
			Extension:   cmd.Extension,
			Name:        cmd.Name,
			Description: cmd.Description,
			Arguments:   args,
		}
	}
	return result, nil
}

// RunExtensionCommand expands an extension command into the prompt it
// stands for.
func (b *Backend) RunExtensionCommand(ctx context.Context, workspaceID, commandID string, args map[string]string) (string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return "", err
	}
	return ws.Extensions.RunCommand(ctx, commandID, args)
}
