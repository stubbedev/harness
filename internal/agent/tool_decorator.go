package agent

import (
	"charm.land/fantasy"
)

// toolDecorator provides passthrough delegation for the AgentTool
// methods a wrapper does not override. Embed it, initialize the
// embedded AgentTool, and implement Run. The promoted Info,
// ProviderOptions and SetProviderOptions satisfy the interface; MCP
// forwards the wrapped tool's server name so callers grouping a built
// tool list by MCP server still see it through the wrapper.
type toolDecorator struct {
	fantasy.AgentTool
}

func (d *toolDecorator) MCP() string {
	if m, ok := d.AgentTool.(interface{ MCP() string }); ok {
		return m.MCP()
	}
	return ""
}
