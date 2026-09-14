package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/config"
)

const MCPResourceToolName = "mcp_resource"

//go:embed mcp_resource.md
var mcpResourceDescription string

// MCPResourceParams covers listing and reading, which differ only by
// whether a URI is known yet.
type MCPResourceParams struct {
	Action  string `json:"action" description:"list (the server's resource URIs) or read (one of them)"`
	MCPName string `json:"mcp_name" description:"Name of a configured MCP server"`
	URI     string `json:"uri,omitempty" description:"read only: the resource URI, exactly as list returned it"`
}

// NewMCPResourceTool folds listing and reading MCP resources into one
// tool, forwarding to the implementation each action had on its own.
func NewMCPResourceTool(cfg *config.ConfigStore) fantasy.AgentTool {
	list, read := NewListMCPResourcesTool(cfg), NewReadMCPResourceTool(cfg)
	return fantasy.NewAgentTool(
		MCPResourceToolName,
		mcpResourceDescription,
		func(ctx context.Context, params MCPResourceParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			var (
				tool  fantasy.AgentTool
				input any
			)
			switch params.Action {
			case "list":
				tool, input = list, ListMCPResourcesParams{MCPName: params.MCPName}
			case "read":
				tool, input = read, ReadMCPResourceParams{MCPName: params.MCPName, URI: params.URI}
			default:
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"unknown action %q. Available: list, read", params.Action)), nil
			}
			encoded, err := json.Marshal(input)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("mcp_resource %s: %w", params.Action, err)
			}
			call.Input = string(encoded)
			call.Name = tool.Info().Name
			return tool.Run(ctx, call)
		},
	)
}
