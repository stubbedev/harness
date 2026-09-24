package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/hooks"
	"github.com/tidwall/sjson"
)

// hookedTool wraps a fantasy.AgentTool to run PreToolUse hooks before
// delegating to the inner tool, and PostToolUse hooks after it completes.
type hookedTool struct {
	toolDecorator
	// queueEpoch, when set, snapshots the session's queued-prompt epoch
	// around the inner run: a user prompt that arrived while the tool was
	// executing is announced in the tool result, so the model reads it on
	// the next step instead of continuing blind.
	queueEpoch func(sessionID string) uint64
	hooks      *hooks.Registry
}

func newHookedTool(inner fantasy.AgentTool, registry *hooks.Registry, queueEpoch func(sessionID string) uint64) *hookedTool {
	return &hookedTool{AgentTool: inner, hooks: registry, queueEpoch: queueEpoch}
}

// wrapToolsWithHooks returns a tool slice with each entry wrapped in a
// hookedTool. Returns the original slice unchanged when there is nothing
// to wrap for: no registry, no hook on either tool event, and no queue
// epoch probe. Sub-agents are wrapped the same as the top-level agent:
// they call the same toolset, so a PreToolUse policy must see their calls
// too. Hooks fired from a sub-agent see the child session's ID.
func wrapToolsWithHooks(tools []fantasy.AgentTool, registry *hooks.Registry, queueEpoch func(sessionID string) uint64) []fantasy.AgentTool {
	if registry == nil && queueEpoch == nil {
		return tools
	}
	if registry != nil && !registry.Has(hooks.EventPreToolUse) && !registry.Has(hooks.EventPostToolUse) && queueEpoch == nil {
		return tools
	}
	out := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		out[i] = newHookedTool(tool, registry, queueEpoch)
	}
	return out
}

func (h *hookedTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	sessionID := tools.GetSessionFromContext(ctx)

	var queueBefore uint64
	if h.queueEpoch != nil && sessionID != "" {
		queueBefore = h.queueEpoch(sessionID)
	}

	var pre *hooks.AggregateResult
	if h.hooks.Has(hooks.EventPreToolUse) {
		result, err := h.hooks.Run(ctx, hooks.EventContext{
			Event:     hooks.EventPreToolUse,
			SessionID: sessionID,
			ToolName:  call.Name,
			ToolInput: call.Input,
		})
		if err != nil {
			slog.Warn("Hook execution error, proceeding with tool call",
				"tool", call.Name, "error", err)
		}
		pre = &result
		if result.Decision == hooks.DecisionDeny || result.Halt {
			return blockedResponse(result), nil
		}
		if result.UpdatedInput != "" {
			call.Input = result.UpdatedInput
		}
	}

	resp, err := h.AgentTool.Run(ctx, call)
	if err != nil {
		return resp, err
	}

	var post *hooks.AggregateResult
	if h.hooks.Has(hooks.EventPostToolUse) {
		result, postErr := h.hooks.Run(ctx, hooks.EventContext{
			Event:     hooks.EventPostToolUse,
			SessionID: sessionID,
			ToolName:  call.Name,
			ToolInput: call.Input,
			ToolResponse: &hooks.ToolResponse{
				Content: resp.Content,
				IsError: resp.IsError,
			},
		})
		if postErr != nil {
			slog.Warn("PostToolUse hook execution error, continuing",
				"tool", call.Name, "error", postErr)
		}
		post = &result
		// The tool already ran, so a deny cannot un-run it: the reason
		// is appended as feedback the model sees and can react to. A
		// halt still ends the turn.
		if result.Decision == hooks.DecisionDeny && result.Reason != "" {
			resp.Content = appendNote(resp.Content, "Hook feedback: "+result.Reason)
		}
		if result.Context != "" {
			resp.Content = appendNote(resp.Content, result.Context)
		}
		if result.Halt {
			resp.StopTurn = true
			if result.Reason != "" {
				resp.Content = appendNote(resp.Content, "Turn halted by hook. Reason: "+result.Reason)
			}
		}
	}

	if pre != nil {
		resp.Metadata = mergeHookMetadata(resp.Metadata, *pre)
	}
	if post != nil && post.HookCount > 0 {
		resp.Metadata = mergeHookMetadata(resp.Metadata, *post)
	}
	if h.queueEpoch != nil && sessionID != "" {
		if arrived := h.queueEpoch(sessionID) - queueBefore; arrived > 0 {
			resp.Content = appendNote(resp.Content, fmt.Sprintf(
				"[%d user message(s) arrived while this tool ran; they will be delivered as the next user message, before your next request]",
				arrived))
		}
	}
	return resp, nil
}

// blockedResponse builds the error response for a denied or halted
// PreToolUse.
func blockedResponse(result hooks.AggregateResult) fantasy.ToolResponse {
	reason := fmt.Sprintf("Tool call blocked by hook. Reason: %s", result.Reason)
	if result.Halt {
		reason = fmt.Sprintf("Turn halted by hook. Reason: %s", result.Reason)
	}
	resp := fantasy.NewTextErrorResponse(reason)
	// Halt ends the whole turn; a plain deny only blocks this tool
	// call so the model can see the error and try something else.
	resp.StopTurn = result.Halt
	resp.Metadata = hookMetadataJSON(result)
	return resp
}

// appendNote appends a note to existing content, keeping the separator
// tidy when either side is empty.
func appendNote(content, note string) string {
	if content == "" {
		return note
	}
	return content + "\n" + note
}

// buildHookMetadata creates a HookMetadata from an AggregateResult.
func buildHookMetadata(result hooks.AggregateResult) hooks.HookMetadata {
	return hooks.HookMetadata{
		HookCount:    result.HookCount,
		Decision:     result.Decision.String(),
		Halt:         result.Halt,
		Reason:       result.Reason,
		InputRewrite: result.UpdatedInput != "",
		Hooks:        result.Hooks,
	}
}

// hookMetadataJSON builds a JSON string containing only the hook metadata.
func hookMetadataJSON(result hooks.AggregateResult) string {
	meta := buildHookMetadata(result)
	data, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return `{"hook":` + string(data) + `}`
}

// mergeHookMetadata injects hook metadata into existing tool metadata.
func mergeHookMetadata(existing string, result hooks.AggregateResult) string {
	if result.HookCount == 0 {
		return existing
	}
	meta := buildHookMetadata(result)
	data, err := json.Marshal(meta)
	if err != nil {
		return existing
	}
	if existing == "" {
		existing = "{}"
	}
	merged, err := sjson.SetRaw(existing, "hook", string(data))
	if err != nil {
		return existing
	}
	return merged
}
