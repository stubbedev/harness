package server

import (
	"context"
	"net/http"

	"github.com/stubbedev/harness/internal/backend"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/proto"
)

// handlePostWorkspaceConfigSet sets a configuration field.
//
//	@Summary		Set a config field
//	@Tags			config
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.ConfigSetRequest	true	"Config set request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/config/set [post]
func (c *controllerV1) handlePostWorkspaceConfigSet(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(_ context.Context, ws *backend.Workspace, req proto.ConfigSetRequest) (any, error) {
		return done(c.backend.SetConfigField(ws.ID, req.Scope, req.Key, req.Value))
	})
}

// handlePostWorkspaceConfigRemove removes a configuration field.
//
//	@Summary		Remove a config field
//	@Tags			config
//	@Accept			json
//	@Param			id		path	string						true	"Workspace ID"
//	@Param			request	body	proto.ConfigRemoveRequest	true	"Config remove request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/config/remove [post]
func (c *controllerV1) handlePostWorkspaceConfigRemove(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(_ context.Context, ws *backend.Workspace, req proto.ConfigRemoveRequest) (any, error) {
		return done(c.backend.RemoveConfigField(ws.ID, req.Scope, req.Key))
	})
}

// handlePostWorkspaceConfigModel updates the preferred model.
//
//	@Summary		Set the preferred model
//	@Tags			config
//	@Accept			json
//	@Param			id		path	string						true	"Workspace ID"
//	@Param			request	body	proto.ConfigModelRequest	true	"Config model request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/config/model [post]
func (c *controllerV1) handlePostWorkspaceConfigModel(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(_ context.Context, ws *backend.Workspace, req proto.ConfigModelRequest) (any, error) {
		return done(c.backend.UpdatePreferredModel(ws.ID, req.Scope, req.ModelType, req.Model))
	})
}

// handlePostWorkspaceConfigCompact sets compact mode.
//
//	@Summary		Set compact mode
//	@Tags			config
//	@Accept			json
//	@Param			id		path	string						true	"Workspace ID"
//	@Param			request	body	proto.ConfigCompactRequest	true	"Config compact request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/config/compact [post]
func (c *controllerV1) handlePostWorkspaceConfigCompact(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(_ context.Context, ws *backend.Workspace, req proto.ConfigCompactRequest) (any, error) {
		return done(c.backend.SetCompactMode(ws.ID, req.Scope, req.Enabled))
	})
}

// handlePostWorkspaceConfigProviderKey sets a provider API key.
//
//	@Summary		Set provider API key
//	@Tags			config
//	@Accept			json
//	@Param			id		path	string							true	"Workspace ID"
//	@Param			request	body	proto.ConfigProviderKeyRequest	true	"Config provider key request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/config/provider-key [post]
func (c *controllerV1) handlePostWorkspaceConfigProviderKey(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBody[proto.ConfigProviderKeyRequest](c, w, r, false)
	if !ok {
		return
	}
	apiKey, err := req.DecodeAPIKey()
	if err != nil {
		c.server.logDebug(r, "Failed to decode api key", "error", err, "kind", req.Kind)
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return done(c.backend.SetProviderAPIKey(ws.ID, req.Scope, req.ProviderID, apiKey))
	})
}

// handlePostWorkspaceConfigImportCopilot imports Copilot credentials.
//
//	@Summary		Import Copilot credentials
//	@Tags			config
//	@Produce		json
//	@Param			id	path		string						true	"Workspace ID"
//	@Success		200	{object}	proto.ImportCopilotResponse
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/config/import-copilot [post]
func (c *controllerV1) handlePostWorkspaceConfigImportCopilot(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		token, ok, err := c.backend.ImportCopilot(ws.ID)
		if err != nil {
			return nil, err
		}
		return proto.ImportCopilotResponse{Token: token, Success: ok}, nil
	})
}

// handlePostWorkspaceConfigRefreshOAuth refreshes an OAuth token for a provider.
//
//	@Summary		Refresh OAuth token
//	@Tags			config
//	@Accept			json
//	@Param			id		path	string							true	"Workspace ID"
//	@Param			request	body	proto.ConfigRefreshOAuthRequest	true	"Refresh OAuth request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/config/refresh-oauth [post]
func (c *controllerV1) handlePostWorkspaceConfigRefreshOAuth(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.ConfigRefreshOAuthRequest) (any, error) {
		return done(c.backend.RefreshOAuthToken(ctx, ws.ID, req.Scope, req.ProviderID))
	})
}

// handleGetWorkspaceProjectInitPrompt returns the project initialization prompt.
//
//	@Summary		Get project initialization prompt
//	@Tags			project
//	@Produce		json
//	@Param			id	path		string							true	"Workspace ID"
//	@Success		200	{object}	proto.ProjectInitPromptResponse
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/project/init-prompt [get]
func (c *controllerV1) handleGetWorkspaceProjectInitPrompt(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		prompt, err := ws.Ops().InitializePrompt()
		if err != nil {
			return nil, err
		}
		return proto.ProjectInitPromptResponse{Prompt: prompt}, nil
	})
}

// handleGetWorkspaceSkills returns the effective visible skills for a workspace.
//
//	@Summary		List visible skills
//	@Tags			skills
//	@Produce		json
//	@Param			id	path		string				true	"Workspace ID"
//	@Success		200	{array}		proto.SkillInfo
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/skills [get]
func (c *controllerV1) handleGetWorkspaceSkills(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		entries, err := ws.Ops().ListSkills(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]proto.SkillInfo, len(entries))
		for i, e := range entries {
			result[i] = proto.SkillInfoFromDomain(e)
		}
		return result, nil
	})
}

// handlePostWorkspaceSkillRead reads a skill's content by ID.
//
//	@Summary		Read skill content
//	@Tags			skills
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string						true	"Workspace ID"
//	@Param			request	body		proto.ReadSkillRequest		true	"Read skill request"
//	@Success		200		{object}	proto.ReadSkillResponse
//	@Failure		400		{object}	proto.Error
//	@Failure		404		{object}	proto.Error
//	@Failure		500		{object}	proto.Error
//	@Router			/workspaces/{id}/skills/read [post]
func (c *controllerV1) handlePostWorkspaceSkillRead(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.ReadSkillRequest) (any, error) {
		content, result, err := ws.Ops().ReadSkill(ctx, req.SkillID)
		if err != nil {
			return nil, err
		}
		return proto.ReadSkillResponse{Content: content, Result: proto.SkillReadResultFromDomain(result)}, nil
	})
}

// handlePostWorkspaceMCPRefreshTools refreshes tools for a named MCP server.
//
//	@Summary		Refresh MCP tools
//	@Tags			mcp
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.MCPNameRequest	true	"MCP name request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/refresh-tools [post]
func (c *controllerV1) handlePostWorkspaceMCPRefreshTools(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.MCPNameRequest) (any, error) {
		ws.Ops().RefreshMCPTools(ctx, req.Name)
		return nil, nil
	})
}

// handlePostWorkspaceMCPReadResource reads a resource from an MCP server.
//
//	@Summary		Read MCP resource
//	@Tags			mcp
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string						true	"Workspace ID"
//	@Param			request	body		proto.MCPReadResourceRequest	true	"MCP read resource request"
//	@Success		200		{array}		proto.MCPResourceContents
//	@Failure		400		{object}	proto.Error
//	@Failure		404		{object}	proto.Error
//	@Failure		500		{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/read-resource [post]
func (c *controllerV1) handlePostWorkspaceMCPReadResource(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.MCPReadResourceRequest) (any, error) {
		return ws.Ops().ReadMCPResource(ctx, req.Name, req.URI)
	})
}

// handleGetWorkspaceMCPPrompts returns the available MCP prompts for a workspace.
//
//	@Summary		Get MCP prompts
//	@Tags			mcp
//	@Produce		json
//	@Param			id	path		string			true	"Workspace ID"
//	@Success		200	{array}		proto.MCPPrompt
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/prompts [get]
func (c *controllerV1) handleGetWorkspaceMCPPrompts(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		prompts, err := ws.Ops().ListMCPPrompts(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]proto.MCPPrompt, len(prompts))
		for i, p := range prompts {
			result[i] = mcpPromptToProto(p)
		}
		return result, nil
	})
}

func mcpPromptToProto(p commands.MCPPrompt) proto.MCPPrompt {
	args := make([]proto.MCPPromptArgument, len(p.Arguments))
	for i, a := range p.Arguments {
		args[i] = proto.MCPPromptArgument{ID: a.ID, Title: a.Title, Description: a.Description, Required: a.Required}
	}
	return proto.MCPPrompt{
		ID:          p.ID,
		Title:       p.Title,
		Description: p.Description,
		PromptID:    p.PromptID,
		ClientID:    p.ClientID,
		Arguments:   args,
	}
}

// handlePostWorkspaceMCPGetPrompt retrieves a prompt from an MCP server.
//
//	@Summary		Get MCP prompt
//	@Tags			mcp
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string						true	"Workspace ID"
//	@Param			request	body		proto.MCPGetPromptRequest	true	"MCP get prompt request"
//	@Success		200		{object}	proto.MCPGetPromptResponse
//	@Failure		400		{object}	proto.Error
//	@Failure		404		{object}	proto.Error
//	@Failure		500		{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/get-prompt [post]
func (c *controllerV1) handlePostWorkspaceMCPGetPrompt(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(_ context.Context, ws *backend.Workspace, req proto.MCPGetPromptRequest) (any, error) {
		prompt, err := ws.Ops().GetMCPPrompt(req.ClientID, req.PromptID, req.Args)
		if err != nil {
			return nil, err
		}
		return proto.MCPGetPromptResponse{Prompt: prompt}, nil
	})
}

// handleGetWorkspaceMCPStates returns the state of all MCP clients.
//
//	@Summary		Get MCP client states
//	@Tags			mcp
//	@Produce		json
//	@Param			id	path		string						true	"Workspace ID"
//	@Success		200	{object}	map[string]proto.MCPClientInfo
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/states [get]
func (c *controllerV1) handleGetWorkspaceMCPStates(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return mapValues(ws.Ops().MCPGetStates(), proto.MCPClientInfoFromDomain), nil
	})
}

// handleGetWorkspaceMCPPendingAuth returns the MCP servers awaiting OAuth
// authentication for a workspace.
//
//	@Summary		Get MCP servers pending OAuth
//	@Tags			mcp
//	@Produce		json
//	@Param			id	path		string	true	"Workspace ID"
//	@Success		200	{array}		proto.MCPPendingAuthServer
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/pending-auth [get]
func (c *controllerV1) handleGetWorkspaceMCPPendingAuth(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		pending := ws.Ops().MCPPendingAuth()
		result := make([]proto.MCPPendingAuthServer, len(pending))
		for i, p := range pending {
			result[i] = proto.MCPPendingAuthServer(p)
		}
		return result, nil
	})
}

// handleGetWorkspaceMCPAuthURL returns the current OAuth authorization URL
// for a named MCP server, if a flow is in progress.
//
//	@Summary		Get MCP OAuth authorization URL
//	@Tags			mcp
//	@Produce		json
//	@Param			id		path	string	true	"Workspace ID"
//	@Param			name	query	string	true	"MCP server name"
//	@Success		200		{object}	proto.MCPAuthResponse
//	@Failure		400		{object}	proto.Error
//	@Failure		404		{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/auth-url [get]
func (c *controllerV1) handleGetWorkspaceMCPAuthURL(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		jsonError(w, http.StatusBadRequest, "name is required")
		return
	}
	c.serve(w, r, func(_ context.Context, ws *backend.Workspace) (any, error) {
		return proto.MCPAuthResponse{AuthURL: ws.Ops().MCPAuthURL(name)}, nil
	})
}

// handlePostWorkspaceMCPAuth runs the OAuth flow for a named MCP server.
// The local browser is suppressed on the server; the client polls
// pending-auth / auth-url to surface the authorization URL on the user's
// machine. The call blocks until the flow completes or the request context
// is cancelled.
//
//	@Summary		Authenticate an MCP server
//	@Tags			mcp
//	@Accept			json
//	@Produce		json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.MCPNameRequest	true	"MCP name request"
//	@Success		200		{object}	proto.MCPAuthResponse
//	@Failure		400		{object}	proto.Error
//	@Failure		404		{object}	proto.Error
//	@Failure		500		{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/auth [post]
func (c *controllerV1) handlePostWorkspaceMCPAuth(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.MCPNameRequest) (any, error) {
		if err := c.backend.MCPAuthenticate(ctx, ws.ID, req.Name); err != nil {
			return nil, err
		}
		// The flow has finished by the time this returns, so there is no
		// in-progress authorization URL to report; the client polls
		// /mcp/auth-url for that while the flow runs.
		return proto.MCPAuthResponse{}, nil
	})
}

// handlePostWorkspaceMCPRefreshPrompts refreshes prompts for a named MCP server.
//
//	@Summary		Refresh MCP prompts
//	@Tags			mcp
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.MCPNameRequest	true	"MCP name request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/refresh-prompts [post]
func (c *controllerV1) handlePostWorkspaceMCPRefreshPrompts(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.MCPNameRequest) (any, error) {
		ws.Ops().MCPRefreshPrompts(ctx, req.Name)
		return nil, nil
	})
}

// handlePostWorkspaceMCPReconnect restarts a named MCP server, clearing
// a session-scoped disable.
//
//	@Summary		Reconnect an MCP server
//	@Tags			mcp
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.MCPNameRequest	true	"MCP name request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/reconnect [post]
func (c *controllerV1) handlePostWorkspaceMCPReconnect(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.MCPNameRequest) (any, error) {
		return done(ws.Ops().MCPReconnect(ctx, req.Name))
	})
}

// handlePostWorkspaceMCPDisable disables a named MCP server for the
// rest of the process without touching its configuration.
//
//	@Summary		Disable an MCP server for this session
//	@Tags			mcp
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.MCPNameRequest	true	"MCP name request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/disable [post]
func (c *controllerV1) handlePostWorkspaceMCPDisable(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.MCPNameRequest) (any, error) {
		return done(ws.Ops().MCPDisableForSession(ctx, req.Name))
	})
}

// handlePostWorkspaceMCPRefreshResources refreshes resources for a named MCP server.
//
//	@Summary		Refresh MCP resources
//	@Tags			mcp
//	@Accept			json
//	@Param			id		path	string					true	"Workspace ID"
//	@Param			request	body	proto.MCPNameRequest	true	"MCP name request"
//	@Success		200
//	@Failure		400	{object}	proto.Error
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/mcp/refresh-resources [post]
func (c *controllerV1) handlePostWorkspaceMCPRefreshResources(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.MCPNameRequest) (any, error) {
		ws.Ops().MCPRefreshResources(ctx, req.Name)
		return nil, nil
	})
}

// handleGetWorkspaceExtensionCommands returns the slash commands the
// workspace's Lua extensions registered.
//
//	@Summary		List extension commands
//	@Tags			extensions
//	@Produce		json
//	@Param			id	path		string	true	"Workspace ID"
//	@Success		200	{array}		proto.ExtensionCommandInfo
//	@Failure		404	{object}	proto.Error
//	@Failure		500	{object}	proto.Error
//	@Router			/workspaces/{id}/extensions/commands [get]
func (c *controllerV1) handleGetWorkspaceExtensionCommands(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		cmds, err := ws.Ops().ListExtensionCommands(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]proto.ExtensionCommandInfo, len(cmds))
		for i, cmd := range cmds {
			result[i] = extensionCommandToProto(cmd)
		}
		return result, nil
	})
}

func extensionCommandToProto(cmd extensions.Command) proto.ExtensionCommandInfo {
	args := make([]proto.ExtensionCommandArgument, len(cmd.Arguments))
	for i, a := range cmd.Arguments {
		args[i] = proto.ExtensionCommandArgument{ID: a.ID, Title: a.Title, Description: a.Description, Required: a.Required}
	}
	return proto.ExtensionCommandInfo{
		ID:          cmd.ID,
		Extension:   cmd.Extension,
		Name:        cmd.Name,
		Description: cmd.Description,
		Arguments:   args,
	}
}

// handlePostWorkspaceExtensionCommandRun expands an extension command
// into the prompt it stands for.
//
//	@Summary		Run an extension command
//	@Tags			extensions
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string								true	"Workspace ID"
//	@Param			request	body		proto.RunExtensionCommandRequest	true	"Run extension command request"
//	@Success		200		{object}	proto.RunExtensionCommandResponse
//	@Failure		400		{object}	proto.Error
//	@Failure		404		{object}	proto.Error
//	@Failure		500		{object}	proto.Error
//	@Router			/workspaces/{id}/extensions/commands/run [post]
func (c *controllerV1) handlePostWorkspaceExtensionCommandRun(w http.ResponseWriter, r *http.Request) {
	serveBody(c, w, r, func(ctx context.Context, ws *backend.Workspace, req proto.RunExtensionCommandRequest) (any, error) {
		prompt, err := ws.Ops().RunExtensionCommand(ctx, req.CommandID, req.Arguments)
		if err != nil {
			return nil, err
		}
		return proto.RunExtensionCommandResponse{Prompt: prompt}, nil
	})
}
