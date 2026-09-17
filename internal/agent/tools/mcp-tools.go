package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/config"
)

// GetMCPTools gets all the currently available MCP tools.
func GetMCPTools(cfg *config.ConfigStore, wd string) []*Tool {
	var result []*Tool
	for mcpName, tools := range mcp.Tools() {
		for _, tool := range tools {
			result = append(result, NewMCPTool(mcpName, tool, cfg, wd))
		}
	}
	return result
}

// NewMCPTool wraps a raw registry tool from the named server as an
// AgentTool. Exported so on-demand loaders (tool search) can wrap a
// single tool the same way GetMCPTools wraps every tool.
func NewMCPTool(mcpName string, tool *mcp.Tool, cfg *config.ConfigStore, wd string) *Tool {
	return &Tool{
		mcpName:    mcpName,
		tool:       tool,
		workingDir: wd,
		cfg:        cfg,
	}
}

// Tool is a tool from a MCP.
type Tool struct {
	mcpName         string
	tool            *mcp.Tool
	cfg             *config.ConfigStore
	workingDir      string
	providerOptions fantasy.ProviderOptions
}

func (m *Tool) SetProviderOptions(opts fantasy.ProviderOptions) {
	m.providerOptions = opts
}

func (m *Tool) ProviderOptions() fantasy.ProviderOptions {
	return m.providerOptions
}

func (m *Tool) Name() string {
	return fmt.Sprintf("mcp_%s_%s", m.mcpName, m.tool.Name)
}

func (m *Tool) MCP() string {
	return m.mcpName
}

func (m *Tool) MCPToolName() string {
	return m.tool.Name
}

func (m *Tool) Info() fantasy.ToolInfo {
	parameters := make(map[string]any)
	required := make([]string, 0)

	if input, ok := m.tool.InputSchema.(map[string]any); ok {
		if props, ok := input["properties"].(map[string]any); ok {
			parameters = props
		}
		if req, ok := input["required"].([]any); ok {
			// Convert []any -> []string when elements are strings
			for _, v := range req {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		} else if reqStr, ok := input["required"].([]string); ok && reqStr != nil {
			// Handle case where it's already []string. A nil one is left
			// alone: Required must marshal as an array, never as null.
			required = reqStr
		}

		// The ToolInfo pipeline only carries the properties map, so any
		// $defs/definitions in the original MCP schema are lost, leaving
		// dangling $ref pointers. Some providers (e.g. Moonshot) validate
		// tool schemas and reject such requests outright. Inline every
		// "#/$defs/..." and "#/definitions/..." reference so the forwarded
		// schema is self-contained.
		defs := map[string]any{}
		for _, key := range []string{"$defs", "definitions"} {
			if d, ok := input[key].(map[string]any); ok {
				maps.Copy(defs, d)
			}
		}
		if len(defs) > 0 {
			parameters = resolveRefs(parameters, defs, 0)
		}
	}

	return fantasy.ToolInfo{
		Name:        m.Name(),
		Description: m.tool.Description,
		Parameters:  parameters,
		Required:    required,
	}
}

const maxRefDepth = 64

// resolveRefs returns a copy of node with local JSON Schema references
// ("#/$defs/Name" and "#/definitions/Name") replaced by deep copies of their
// targets. The input MCP schema is cached and shared, so nothing is mutated
// in place. Depth is capped to guard against cyclic definitions.
func resolveRefs(node map[string]any, defs map[string]any, depth int) map[string]any {
	if depth > maxRefDepth {
		return node
	}
	result := make(map[string]any, len(node))
	for key, value := range node {
		switch child := value.(type) {
		case map[string]any:
			if resolved, ok := resolveRef(child, defs, depth); ok {
				result[key] = resolved
				continue
			}
			result[key] = resolveRefs(child, defs, depth+1)
		case []any:
			items := make([]any, len(child))
			for i, item := range child {
				switch elem := item.(type) {
				case map[string]any:
					if resolved, ok := resolveRef(elem, defs, depth); ok {
						items[i] = resolved
						continue
					}
					items[i] = resolveRefs(elem, defs, depth+1)
				default:
					items[i] = elem
				}
			}
			result[key] = items
		default:
			result[key] = value
		}
	}
	return result
}

// resolveRef checks whether node is a local reference and, if so, returns a
// resolved deep copy of its target.
func resolveRef(node map[string]any, defs map[string]any, depth int) (map[string]any, bool) {
	ref, ok := node["$ref"].(string)
	if !ok || len(node) != 1 {
		return nil, false
	}
	var name string
	for _, prefix := range []string{"#/$defs/", "#/definitions/"} {
		if after, found := strings.CutPrefix(ref, prefix); found {
			name = after
			break
		}
	}
	if name == "" {
		return nil, false
	}
	target, ok := defs[name].(map[string]any)
	if !ok {
		return nil, false
	}
	return resolveRefs(deepCopyMap(target), defs, depth+1), true
}

func deepCopyMap(source map[string]any) map[string]any {
	data, err := json.Marshal(source)
	if err != nil {
		return map[string]any{}
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]any{}
	}
	return result
}

func (m *Tool) Run(ctx context.Context, params fantasy.ToolCall) (fantasy.ToolResponse, error) {
	sessionID := GetSessionFromContext(ctx)
	if sessionID == "" {
		return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for creating a new file")
	}

	result, err := mcp.RunTool(ctx, m.cfg, m.mcpName, m.tool.Name, params.Input)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	switch result.Type {
	case "image", "media":
		if !GetSupportsImagesFromContext(ctx) {
			modelName := GetModelNameFromContext(ctx)
			return fantasy.NewTextErrorResponse(fmt.Sprintf("This model (%s) does not support image data.", modelName)), nil
		}

		var response fantasy.ToolResponse
		if result.Type == "image" {
			response = fantasy.NewImageResponse(result.Data, result.MediaType)
		} else {
			response = fantasy.NewMediaResponse(result.Data, result.MediaType)
		}
		response.Content = result.Content
		return response, nil
	default:
		return fantasy.NewTextResponse(result.Content), nil
	}
}
