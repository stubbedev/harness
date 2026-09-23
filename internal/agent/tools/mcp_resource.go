package tools

import (
	"context"
	_ "embed"
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
	list, read := listMCPResourcesAction(cfg), readMCPResourceAction(cfg)
	return fantasy.NewParallelAgentTool(
		MCPResourceToolName,
		mcpResourceDescription,
		func(ctx context.Context, params MCPResourceParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			switch params.Action {
			case "list":
				return list(ctx, ListMCPResourcesParams{MCPName: params.MCPName})
			case "read":
				return read(ctx, ReadMCPResourceParams{MCPName: params.MCPName, URI: params.URI})
			default:
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"unknown action %q. Available: list, read", params.Action)), nil
			}
		},
	)
}
