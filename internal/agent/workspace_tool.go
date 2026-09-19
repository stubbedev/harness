package agent

import (
	"context"
	"encoding/json"
	"path/filepath"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools"
)

type workspaceLSPTool struct {
	fantasy.AgentTool
	root string
}

func (t workspaceLSPTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var input map[string]json.RawMessage
	if json.Unmarshal([]byte(call.Input), &input) != nil || input == nil {
		return t.AgentTool.Run(ctx, call)
	}
	for _, field := range []string{"path", "file_path"} {
		var path string
		if value, exists := input[field]; exists {
			if json.Unmarshal(value, &path) != nil {
				continue
			}
		} else if field == "file_path" {
			continue
		}
		if path == "" && field == "file_path" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(t.root, path)
		}
		input[field], _ = json.Marshal(path)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	call.Input = string(encoded)
	return t.AgentTool.Run(ctx, call)
}

func workspaceTools(input []fantasy.AgentTool, root string) []fantasy.AgentTool {
	for i, tool := range input {
		if tool.Info().Name == tools.LSPToolName {
			input[i] = workspaceLSPTool{AgentTool: tool, root: root}
		}
	}
	return input
}
