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
	toolDecorator
}

// wrapToolsResilient wraps every tool in the slice with resilientTool.
func wrapToolsResilient(tools []fantasy.AgentTool) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		out[i] = &resilientTool{AgentTool: tool}
	}
	return out
}

func (t *resilientTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	resp, err := t.AgentTool.Run(ctx, call)
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return resp, err
	}
	resp.IsError = true
	resp.Content = err.Error()
	return resp, nil
}
