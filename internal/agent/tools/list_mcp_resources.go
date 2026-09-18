package tools

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/config"
)

type ListMCPResourcesParams struct {
	MCPName string `json:"mcp_name" description:"Name of a configured MCP server"`
}

const ListMCPResourcesToolName = "list_mcp_resources"

//go:embed list_mcp_resources.md
var listMCPResourcesDescription string

func NewListMCPResourcesTool(cfg *config.ConfigStore) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		ListMCPResourcesToolName,
		listMCPResourcesDescription,
		func(ctx context.Context, params ListMCPResourcesParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			params.MCPName = strings.TrimSpace(params.MCPName)
			if params.MCPName == "" {
				return fantasy.NewTextErrorResponse("mcp_name parameter is required"), nil
			}

			if _, err := SessionIDOrError(ctx, "listing MCP resources"); err != nil {
				return fantasy.ToolResponse{}, err
			}

			resources, err := mcp.ListResources(ctx, cfg, params.MCPName)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if len(resources) == 0 {
				return fantasy.NewTextResponse("No resources found"), nil
			}

			lines := make([]string, 0, len(resources))
			for _, resource := range resources {
				if resource == nil {
					continue
				}
				title := cmp.Or(resource.Title, resource.Name, resource.URI)
				line := fmt.Sprintf("- %s", title)
				if resource.URI != "" {
					line = fmt.Sprintf("%s (%s)", line, resource.URI)
				}
				if resource.Description != "" {
					line = fmt.Sprintf("%s: %s", line, resource.Description)
				}
				if resource.MIMEType != "" {
					line = fmt.Sprintf("%s [mime: %s]", line, resource.MIMEType)
				}
				if resource.Size > 0 {
					line = fmt.Sprintf("%s [size: %d]", line, resource.Size)
				}
				lines = append(lines, line)
			}

			sort.Strings(lines)
			return fantasy.NewTextResponse(strings.Join(lines, "\n")), nil
		},
	)
}
