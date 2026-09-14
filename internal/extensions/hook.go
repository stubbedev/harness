package extensions

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/stubbedev/harness/internal/hooks"
	lua "github.com/yuin/gopher-lua"
)

// Has reports whether any loaded extension handles the event. It is half
// of hooks.Dispatcher: a registry asks before building a payload.
func (h *Host) Has(event string) bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, in := range h.instances {
		if len(in.hooks[event]) > 0 {
			return true
		}
	}
	return false
}

// Dispatch runs every handler registered for the event and returns one
// result per handler, in load order. Handlers run sequentially rather
// than in parallel: each VM is single-threaded anyway, and sequential
// execution makes the order of their decisions predictable.
//
// A context already inside the extension system dispatches nothing. A
// handler reached from a host function of the same extension would
// re-enter a VM that is mid-call and deadlock.
func (h *Host) Dispatch(ctx context.Context, ec hooks.EventContext) []hooks.DispatchResult {
	if h == nil || guarded(ctx) {
		return nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	subject := ec.Subject()
	payload := hooks.BuildEventPayload(ec)

	var results []hooks.DispatchResult
	for _, in := range h.instances {
		for _, handler := range in.hooks[ec.Event] {
			if handler.matcher != nil && !handler.matcher.MatchString(subject) {
				continue
			}
			results = append(results, hooks.DispatchResult{
				Name:    in.ext.Name + ":" + ec.Event,
				Matcher: handlerMatcher(handler),
				Result:  in.runHook(ctx, handler, payload),
			})
		}
	}
	return results
}

func handlerMatcher(handler *hookHandler) string {
	if handler.matcher == nil {
		return ""
	}
	return handler.matcher.String()
}

// runHook calls one handler with the event payload and reads its verdict.
func (in *instance) runHook(ctx context.Context, handler *hookHandler, payload []byte) hooks.HookResult {
	event, err := jsonToLua(in.L, payload)
	if err != nil {
		slog.Warn("Failed to build hook payload for extension", "extension", in.ext.Name, "error", err)
		return hooks.HookResult{Decision: hooks.DecisionNone}
	}

	value, err := in.call(withDispatchGuard(ctx), handler.fn, event)
	if err != nil {
		slog.Warn(
			"Extension hook handler failed",
			"extension", in.ext.Name,
			"event", handler.event,
			"error", err,
		)
		return hooks.HookResult{Decision: hooks.DecisionNone}
	}
	return hookResult(value)
}

// hookResult renders a handler's return value as a hook decision. A
// handler that returns nothing expresses no opinion; a string is added
// to the model's context; a table is the full verdict.
func hookResult(value lua.LValue) hooks.HookResult {
	switch typed := value.(type) {
	case *lua.LNilType:
		return hooks.HookResult{Decision: hooks.DecisionNone}
	case lua.LString:
		return hooks.HookResult{Decision: hooks.DecisionNone, Context: string(typed)}
	case *lua.LTable:
		result := hooks.HookResult{
			Decision: parseDecision(tableString(typed, "decision", "")),
			Halt:     tableBool(typed, "halt", false),
			Reason:   tableString(typed, "reason", ""),
			Context:  tableString(typed, "context", ""),
		}
		result.UpdatedPrompt = tableString(typed, "updated_prompt", "")
		if patch, ok := typed.RawGetString("updated_input").(*lua.LTable); ok {
			if data, err := json.Marshal(fromLua(patch)); err == nil {
				result.UpdatedInput = string(data)
			}
		}
		return result
	default:
		return hooks.HookResult{Decision: hooks.DecisionNone}
	}
}

func parseDecision(value string) hooks.Decision {
	switch value {
	case "deny", "block":
		return hooks.DecisionDeny
	case "allow":
		return hooks.DecisionAllow
	default:
		return hooks.DecisionNone
	}
}
