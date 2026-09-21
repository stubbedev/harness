package agent

import (
	"context"
	"errors"

	"charm.land/fantasy"
)

// resilientTool turns a failed inner run into an error response the
// model reads as the tool result and can recover from. The agent loop
// treats a Go error from a tool as fatal and ends the turn, leaving the
// user to resume the conversation by hand; an error response keeps the
// loop running so the model corrects the call itself. Context
// cancellation stays an error so a canceled turn keeps unwinding
// instead of reaching the provider one more time.
type resilientTool struct {
	inner fantasy.AgentTool
}

// wrapToolsResilient wraps every tool in the slice with resilientTool.
func wrapToolsResilient(tools []fantasy.AgentTool) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		out[i] = &resilientTool{inner: tool}
	}
	return out
}

func (t *resilientTool) Info() fantasy.ToolInfo {
	return t.inner.Info()
}

func (t *resilientTool) ProviderOptions() fantasy.ProviderOptions {
	return t.inner.ProviderOptions()
}

// MCP forwards the wrapped tool's server name so callers grouping a
// built tool list by server still see it through the wrapper.
func (t *resilientTool) MCP() string {
	if inner, ok := t.inner.(interface{ MCP() string }); ok {
		return inner.MCP()
	}
	return ""
}

func (t *resilientTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	t.inner.SetProviderOptions(opts)
}

func (t *resilientTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	resp, err := t.inner.Run(ctx, call)
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return resp, err
	}
	resp.IsError = true
	resp.Content = err.Error()
	return resp, nil
}
