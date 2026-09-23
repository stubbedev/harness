package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/proto"
)

// SetConfigField sets a config key/value pair on the server.
func (c *Client) SetConfigField(ctx context.Context, id string, scope config.Scope, key string, value any) error {
	return c.do(ctx, "set config field", post(wsPath(id, "config", "set"), proto.ConfigSetRequest{Scope: scope, Key: key, Value: value}))
}

// RemoveConfigField removes a config key on the server.
func (c *Client) RemoveConfigField(ctx context.Context, id string, scope config.Scope, key string) error {
	return c.do(ctx, "remove config field", post(wsPath(id, "config", "remove"), proto.ConfigRemoveRequest{Scope: scope, Key: key}))
}

// UpdatePreferredModel updates the preferred model on the server.
func (c *Client) UpdatePreferredModel(ctx context.Context, id string, scope config.Scope, modelType config.SelectedModelType, model config.SelectedModel) error {
	return c.do(ctx, "update preferred model", post(wsPath(id, "config", "model"), proto.ConfigModelRequest{
		Scope:     scope,
		ModelType: modelType,
		Model:     model,
	}))
}

// SetCompactMode sets compact mode on the server.
func (c *Client) SetCompactMode(ctx context.Context, id string, scope config.Scope, enabled bool) error {
	return c.do(ctx, "set compact mode", post(wsPath(id, "config", "compact"), proto.ConfigCompactRequest{Scope: scope, Enabled: enabled}))
}

// SetProviderAPIKey sets a provider API key on the server. The wire
// format tags the credential with an explicit Kind so the server can
// decode it back into the right Go type — JSON's `any` loses that
// information across the socket.
func (c *Client) SetProviderAPIKey(ctx context.Context, id string, scope config.Scope, providerID string, apiKey any) error {
	var (
		kind proto.APIKeyKind
		raw  json.RawMessage
	)
	switch v := apiKey.(type) {
	case string:
		kind = proto.APIKeyKindString
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("failed to marshal api key string: %w", err)
		}
		raw = b
	case *oauth.Token:
		if v == nil {
			return fmt.Errorf("oauth token is nil")
		}
		kind = proto.APIKeyKindOAuth
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("failed to marshal oauth token: %w", err)
		}
		raw = b
	default:
		return fmt.Errorf("unsupported api key type %T", apiKey)
	}

	return c.do(ctx, "set provider API key", post(wsPath(id, "config", "provider-key"), proto.ConfigProviderKeyRequest{
		Scope:      scope,
		ProviderID: providerID,
		Kind:       kind,
		APIKey:     raw,
	}))
}

// ImportCopilot attempts to import a GitHub Copilot token on the
// server.
func (c *Client) ImportCopilot(ctx context.Context, id string) (*oauth.Token, bool, error) {
	resp, err := call[proto.ImportCopilotResponse](ctx, c, "import copilot", post(wsPath(id, "config", "import-copilot"), nil))
	return resp.Token, resp.Success, err
}

// RefreshOAuthToken refreshes an OAuth token for a provider on the
// server.
func (c *Client) RefreshOAuthToken(ctx context.Context, id string, scope config.Scope, providerID string) error {
	return c.do(ctx, "refresh OAuth token", post(wsPath(id, "config", "refresh-oauth"), proto.ConfigRefreshOAuthRequest{
		Scope:      scope,
		ProviderID: providerID,
	}))
}

// GetInitializePrompt retrieves the initialization prompt from the
// server.
func (c *Client) GetInitializePrompt(ctx context.Context, id string) (string, error) {
	resp, err := call[proto.ProjectInitPromptResponse](ctx, c, "get init prompt", get(wsPath(id, "project", "init-prompt")))
	return resp.Prompt, err
}

// ListSkills retrieves the visible skills for a workspace.
func (c *Client) ListSkills(ctx context.Context, id string) ([]proto.SkillInfo, error) {
	return call[[]proto.SkillInfo](ctx, c, "list skills", get(wsPath(id, "skills")))
}

// ReadSkill reads a skill's content by ID from the server.
func (c *Client) ReadSkill(ctx context.Context, id, skillID string) (*proto.ReadSkillResponse, error) {
	resp, err := call[proto.ReadSkillResponse](ctx, c, "read skill", post(wsPath(id, "skills", "read"), proto.ReadSkillRequest{SkillID: skillID}))
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// RefreshMCPTools refreshes tools for a named MCP server.
func (c *Client) RefreshMCPTools(ctx context.Context, id, name string) error {
	return c.do(ctx, "refresh MCP tools", post(wsPath(id, "mcp", "refresh-tools"), proto.MCPNameRequest{Name: name}))
}

// ReadMCPResource reads a resource from a named MCP server.
func (c *Client) ReadMCPResource(ctx context.Context, id, name, uri string) ([]proto.MCPResourceContents, error) {
	return call[[]proto.MCPResourceContents](ctx, c, "read MCP resource", post(wsPath(id, "mcp", "read-resource"), proto.MCPReadResourceRequest{
		Name: name,
		URI:  uri,
	}))
}

// ListMCPPrompts retrieves the MCP prompts available to a workspace.
func (c *Client) ListMCPPrompts(ctx context.Context, id string) ([]proto.MCPPrompt, error) {
	return call[[]proto.MCPPrompt](ctx, c, "list MCP prompts", get(wsPath(id, "mcp", "prompts")))
}

// GetMCPPrompt retrieves a prompt from a named MCP server.
func (c *Client) GetMCPPrompt(ctx context.Context, id, clientID, promptID string, args map[string]string) (string, error) {
	resp, err := call[proto.MCPGetPromptResponse](ctx, c, "get MCP prompt", post(wsPath(id, "mcp", "get-prompt"), proto.MCPGetPromptRequest{
		ClientID: clientID,
		PromptID: promptID,
		Args:     args,
	}))
	return resp.Prompt, err
}

// ListExtensionCommands retrieves the extension commands for a workspace.
func (c *Client) ListExtensionCommands(ctx context.Context, id string) ([]proto.ExtensionCommandInfo, error) {
	return call[[]proto.ExtensionCommandInfo](ctx, c, "list extension commands", get(wsPath(id, "extensions", "commands")))
}

// RunExtensionCommand expands an extension command into its prompt.
func (c *Client) RunExtensionCommand(ctx context.Context, id, commandID string, args map[string]string) (string, error) {
	resp, err := call[proto.RunExtensionCommandResponse](ctx, c, "run extension command", post(wsPath(id, "extensions", "commands", "run"), proto.RunExtensionCommandRequest{
		CommandID: commandID,
		Arguments: args,
	}))
	return resp.Prompt, err
}
