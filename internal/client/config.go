package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/proto"
)

// SetConfigField sets a config key/value pair on the server.
func (c *Client) SetConfigField(ctx context.Context, id string, scope config.Scope, key string, value any) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/config/set", id), nil, jsonBody(struct {
		Scope config.Scope `json:"scope"`
		Key   string       `json:"key"`
		Value any          `json:"value"`
	}{Scope: scope, Key: key, Value: value}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to set config field: %w", err)
	}
	if err := okOrError(rsp, "failed to set config field"); err != nil {
		return err
	}
	return nil
}

// RemoveConfigField removes a config key on the server.
func (c *Client) RemoveConfigField(ctx context.Context, id string, scope config.Scope, key string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/config/remove", id), nil, jsonBody(struct {
		Scope config.Scope `json:"scope"`
		Key   string       `json:"key"`
	}{Scope: scope, Key: key}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to remove config field: %w", err)
	}
	if err := okOrError(rsp, "failed to remove config field"); err != nil {
		return err
	}
	return nil
}

// UpdatePreferredModel updates the preferred model on the server.
func (c *Client) UpdatePreferredModel(ctx context.Context, id string, scope config.Scope, modelType config.SelectedModelType, model config.SelectedModel) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/config/model", id), nil, jsonBody(struct {
		Scope     config.Scope             `json:"scope"`
		ModelType config.SelectedModelType `json:"model_type"`
		Model     config.SelectedModel     `json:"model"`
	}{Scope: scope, ModelType: modelType, Model: model}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to update preferred model: %w", err)
	}
	if err := okOrError(rsp, "failed to update preferred model"); err != nil {
		return err
	}
	return nil
}

// SetCompactMode sets compact mode on the server.
func (c *Client) SetCompactMode(ctx context.Context, id string, scope config.Scope, enabled bool) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/config/compact", id), nil, jsonBody(struct {
		Scope   config.Scope `json:"scope"`
		Enabled bool         `json:"enabled"`
	}{Scope: scope, Enabled: enabled}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to set compact mode: %w", err)
	}
	if err := okOrError(rsp, "failed to set compact mode"); err != nil {
		return err
	}
	return nil
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

	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/config/provider-key", id), nil, jsonBody(proto.ConfigProviderKeyRequest{
		Scope:      scope,
		ProviderID: providerID,
		Kind:       kind,
		APIKey:     raw,
	}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to set provider API key: %w", err)
	}
	if err := okOrError(rsp, "failed to set provider API key"); err != nil {
		return err
	}
	return nil
}

// ImportCopilot attempts to import a GitHub Copilot token on the
// server.
func (c *Client) ImportCopilot(ctx context.Context, id string) (*oauth.Token, bool, error) {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/config/import-copilot", id), nil, nil, nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to import copilot: %w", err)
	}
	var result struct {
		Token   *oauth.Token `json:"token"`
		Success bool         `json:"success"`
	}
	if err := decodeJSON(rsp, &result, "failed to import copilot", "import copilot response"); err != nil {
		return nil, false, err
	}
	return result.Token, result.Success, nil
}

// RefreshOAuthToken refreshes an OAuth token for a provider on the
// server.
func (c *Client) RefreshOAuthToken(ctx context.Context, id string, scope config.Scope, providerID string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/config/refresh-oauth", id), nil, jsonBody(struct {
		Scope      config.Scope `json:"scope"`
		ProviderID string       `json:"provider_id"`
	}{Scope: scope, ProviderID: providerID}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to refresh OAuth token: %w", err)
	}
	if err := okOrError(rsp, "failed to refresh OAuth token"); err != nil {
		return err
	}
	return nil
}

// GetInitializePrompt retrieves the initialization prompt from the
// server.
func (c *Client) GetInitializePrompt(ctx context.Context, id string) (string, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/project/init-prompt", id), nil, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get init prompt: %w", err)
	}
	var result struct {
		Prompt string `json:"prompt"`
	}
	if err := decodeJSON(rsp, &result, "failed to get init prompt", "init prompt response"); err != nil {
		return "", err
	}
	return result.Prompt, nil
}

// ListSkills retrieves the visible skills for a workspace.
func (c *Client) ListSkills(ctx context.Context, id string) ([]proto.SkillInfo, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/skills", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list skills: %w", err)
	}
	var skills []proto.SkillInfo
	if err := decodeJSON(rsp, &skills, "failed to list skills", "skills"); err != nil {
		return nil, err
	}
	return skills, nil
}

// ReadSkill reads a skill's content by ID from the server.
func (c *Client) ReadSkill(ctx context.Context, id, skillID string) (*proto.ReadSkillResponse, error) {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/skills/read", id), nil, jsonBody(proto.ReadSkillRequest{
		SkillID: skillID,
	}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return nil, fmt.Errorf("failed to read skill: %w", err)
	}
	var result proto.ReadSkillResponse
	if err := decodeJSON(rsp, &result, "failed to read skill", "skill response"); err != nil {
		return nil, err
	}
	return &result, nil
}

// MCPResourceContents holds the contents of an MCP resource.
type MCPResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     []byte `json:"blob,omitempty"`
}

// RefreshMCPTools refreshes tools for a named MCP server.
func (c *Client) RefreshMCPTools(ctx context.Context, id, name string) error {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/mcp/refresh-tools", id), nil, jsonBody(struct {
		Name string `json:"name"`
	}{Name: name}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return fmt.Errorf("failed to refresh MCP tools: %w", err)
	}
	if err := okOrError(rsp, "failed to refresh MCP tools"); err != nil {
		return err
	}
	return nil
}

// ReadMCPResource reads a resource from a named MCP server.
func (c *Client) ReadMCPResource(ctx context.Context, id, name, uri string) ([]MCPResourceContents, error) {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/mcp/read-resource", id), nil, jsonBody(struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	}{Name: name, URI: uri}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return nil, fmt.Errorf("failed to read MCP resource: %w", err)
	}
	var contents []MCPResourceContents
	if err := decodeJSON(rsp, &contents, "failed to read MCP resource", "MCP resource"); err != nil {
		return nil, err
	}
	return contents, nil
}

func (c *Client) ListMCPPrompts(ctx context.Context, id string) ([]proto.MCPPrompt, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/mcp/prompts", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCP prompts: %w", err)
	}
	var prompts []proto.MCPPrompt
	if err := decodeJSON(rsp, &prompts, "failed to list MCP prompts", "MCP prompts"); err != nil {
		return nil, err
	}
	return prompts, nil
}

// GetMCPPrompt retrieves a prompt from a named MCP server.
func (c *Client) GetMCPPrompt(ctx context.Context, id, clientID, promptID string, args map[string]string) (string, error) {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/mcp/get-prompt", id), nil, jsonBody(struct {
		ClientID string            `json:"client_id"`
		PromptID string            `json:"prompt_id"`
		Args     map[string]string `json:"args"`
	}{ClientID: clientID, PromptID: promptID, Args: args}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return "", fmt.Errorf("failed to get MCP prompt: %w", err)
	}
	var result struct {
		Prompt string `json:"prompt"`
	}
	if err := decodeJSON(rsp, &result, "failed to get MCP prompt", "MCP prompt response"); err != nil {
		return "", err
	}
	return result.Prompt, nil
}

// ListExtensionCommands retrieves the extension commands for a workspace.
func (c *Client) ListExtensionCommands(ctx context.Context, id string) ([]proto.ExtensionCommandInfo, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/extensions/commands", id), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list extension commands: %w", err)
	}
	var commands []proto.ExtensionCommandInfo
	if err := decodeJSON(rsp, &commands, "failed to list extension commands", "extension commands"); err != nil {
		return nil, err
	}
	return commands, nil
}

// RunExtensionCommand expands an extension command into its prompt.
func (c *Client) RunExtensionCommand(ctx context.Context, id, commandID string, args map[string]string) (string, error) {
	rsp, err := c.post(ctx, fmt.Sprintf("/workspaces/%s/extensions/commands/run", id), nil, jsonBody(proto.RunExtensionCommandRequest{
		CommandID: commandID,
		Arguments: args,
	}), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		return "", fmt.Errorf("failed to run extension command: %w", err)
	}
	var result proto.RunExtensionCommandResponse
	if err := decodeJSON(rsp, &result, "failed to run extension command", "extension command response"); err != nil {
		return "", err
	}
	return result.Prompt, nil
}
