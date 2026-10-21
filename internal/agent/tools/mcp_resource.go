package tools

import (
	"context"
	_ "embed"

	"github.com/stubbedev/harness/internal/toolname"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/toolaction"
)

const MCPResourceToolName = toolname.MCPResource

//go:embed mcp_resource.md
var mcpResourceDescription string

// MCPResourceParams covers listing and reading, which differ only by
// whether a URI is known yet.
type MCPResourceParams struct {
	Action  MCPResourceAction `json:"action" description:"list (the server's resource URIs) or read (one of them)"`
	MCPName string            `json:"mcp_name" description:"Name of a configured MCP server"`
	URI     string            `json:"uri,omitempty" description:"read only: the resource URI, exactly as list returned it"`
}

type MCPResourceAction string

const (
	MCPResourceActionList MCPResourceAction = "list"
	MCPResourceActionRead MCPResourceAction = "read"
)

type mcpResourceActionFunc func(context.Context, MCPResourceParams) (fantasy.ToolResponse, error)

// NewMCPResourceTool folds listing and reading MCP resources into one
// tool, forwarding to the implementation each action had on its own.
func NewMCPResourceTool(cfg *config.ConfigStore) fantasy.AgentTool {
	list, read := listMCPResourcesAction(cfg), readMCPResourceAction(cfg)
	actions := toolaction.New("action", map[MCPResourceAction]mcpResourceActionFunc{
		MCPResourceActionList: func(ctx context.Context, params MCPResourceParams) (fantasy.ToolResponse, error) {
			return list(ctx, ListMCPResourcesParams{MCPName: params.MCPName})
		},
		MCPResourceActionRead: func(ctx context.Context, params MCPResourceParams) (fantasy.ToolResponse, error) {
			return read(ctx, ReadMCPResourceParams{MCPName: params.MCPName, URI: params.URI})
		},
	})
	return toolaction.Advertise(fantasy.NewParallelAgentTool(
		MCPResourceToolName,
		mcpResourceDescription,
		func(ctx context.Context, params MCPResourceParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			action, err := actions.Lookup(params.Action)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return action(ctx, params)
		},
	), actions.Enum())
}
