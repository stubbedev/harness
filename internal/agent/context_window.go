package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/config"
)

// errContextWindowExceeded is returned by the run's PrepareStep when the
// request it just assembled is projected to be larger than the usable
// context window. The run's error path recognizes it, summarizes the
// session, and requeues the prompt once instead of sending a request the
// provider is guaranteed to reject.
var errContextWindowExceeded = errors.New("context window exceeded")

// usableContextWindow returns the portion of the model's context window
// available for input: the window minus the output reservation. Without
// subtracting it the auto-summarize threshold can sit above an input
// ceiling the provider enforces, and never fire.
//
// The arithmetic lives in config so the UI's context meter measures
// against the same budget the agent plans against; see
// [config.UsableContextWindow].
func usableContextWindow(model Model) int64 {
	return config.UsableContextWindow(model.CatalogCfg, model.ModelCfg)
}

// contextLengthErrorMarkers are lowercase substrings that identify a
// provider rejecting a request for exceeding the context window. The
// exact wording varies per provider, so match loosely.
var contextLengthErrorMarkers = []string{
	"context_length",
	"context length",
	"context window",
	"maximum context",
	"prompt is too long",
	"prompt too long",
	"too many input tokens",
	"input tokens exceed",
	"exceeds the model's context",
	"input length and `max_tokens` exceed",
}

// isContextLengthError reports whether err is a provider rejection of a
// request that did not fit the context window.
func isContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range contextLengthErrorMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// maxToolResultChars caps a single tool result before it reaches the
// model. One uncapped result can consume a whole context window on its
// own, which no auto-summarize trigger can recover from gracefully.
const maxToolResultChars = 100_000

// truncateToolResponse caps a text tool result at maxToolResultChars and
// appends a marker telling the model what was dropped and how to get it.
// Binary (media) responses are passed through untouched.
func truncateToolResponse(response fantasy.ToolResponse) fantasy.ToolResponse {
	if len(response.Data) != 0 || len(response.Content) <= maxToolResultChars {
		return response
	}
	truncated := response
	cut := strings.ToValidUTF8(response.Content[:maxToolResultChars], "")
	truncated.Content = fmt.Sprintf(
		"%s\n\n(result truncated: %d of %d characters shown — narrow the request (filter, offset, or paginate) to see the rest)",
		cut, len(cut), len(response.Content),
	)
	return truncated
}

// resultCappingTool wraps an AgentTool so oversized text results are
// truncated before they enter the message history and the next request.
type resultCappingTool struct {
	fantasy.AgentTool
}

func (t resultCappingTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	response, err := t.AgentTool.Run(ctx, call)
	if err == nil {
		response = truncateToolResponse(response)
	}
	return response, err
}

// withResultCap wraps every tool so no single result can exceed
// maxToolResultChars. It is applied where the agent's tool palette is
// installed, so it covers built-in and MCP tools alike.
func withResultCap(tools []fantasy.AgentTool) []fantasy.AgentTool {
	if len(tools) == 0 {
		return tools
	}
	capped := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		capped[i] = resultCappingTool{AgentTool: tool}
	}
	return capped
}
