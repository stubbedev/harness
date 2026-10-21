// Package hooks runs user-defined shell commands that fire on hook events
// (e.g. PreToolUse), returning decisions that control agent behavior.
package hooks

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"strings"

	"github.com/stubbedev/harness/internal/config"
)

// Hook event name constants, re-exported from config, which owns the one
// list of events. See the config.Hook* constants for what each means.
const (
	EventPreToolUse       = config.HookPreToolUse
	EventPostToolUse      = config.HookPostToolUse
	EventUserPromptSubmit = config.HookUserPromptSubmit
	EventSessionStart     = config.HookSessionStart
	EventStop             = config.HookStop
	EventSubagentStop     = config.HookSubagentStop
	EventNotification     = config.HookNotification
	EventPreCompact       = config.HookPreCompact
	EventPostCompact      = config.HookPostCompact
)

// HaltExitCode is the exit code that halts the whole turn. 2 blocks the
// current tool call; 49 sits in the no-man's-land between the
// generic-error range (1-30), the sysexits range (64-78), and the
// killed-by-signal range (128+) so it can't be hit by accident.
const HaltExitCode = 49

// HookMetadata is embedded in tool response metadata so the UI can
// display a hook indicator.
type HookMetadata struct {
	HookCount    int        `json:"hook_count"`
	Decision     string     `json:"decision"`
	Halt         bool       `json:"halt,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	InputRewrite bool       `json:"input_rewrite,omitempty"`
	Hooks        []HookInfo `json:"hooks,omitempty"`
}

// HookInfo identifies a single hook that ran and its individual result.
type HookInfo struct {
	Name         string `json:"name"`
	Matcher      string `json:"matcher,omitempty"`
	Decision     string `json:"decision"`
	Halt         bool   `json:"halt,omitempty"`
	Reason       string `json:"reason,omitempty"`
	InputRewrite bool   `json:"input_rewrite,omitempty"`
}

// Decision represents the outcome of a single hook execution.
type Decision int

const (
	// DecisionNone means the hook expressed no opinion.
	DecisionNone Decision = iota
	// DecisionAllow means the hook explicitly allowed the action.
	DecisionAllow
	// DecisionDeny means the hook blocked the action.
	DecisionDeny
	DecisionInvalid
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	case DecisionInvalid:
		return "invalid"
	default:
		return "none"
	}
}

func InvalidResult(err error) HookResult {
	return HookResult{Decision: DecisionInvalid, Reason: err.Error()}
}

// HookResult holds the parsed output of a single hook execution.
type HookResult struct {
	Decision      Decision
	Halt          bool   // If true, halt the whole turn.
	Reason        string // Deny or halt reason (same field, different audience).
	Context       string
	UpdatedInput  string // Shallow-merge patch against tool_input (opaque JSON).
	UpdatedPrompt string // Full replacement for the user prompt (UserPromptSubmit).
}

// AggregateResult holds the combined outcome of all hooks for an event.
type AggregateResult struct {
	Decision      Decision
	Halt          bool       // Any hook requested halt.
	HookCount     int        // Number of hooks that ran.
	Hooks         []HookInfo // Info about each hook that ran (config order).
	Reason        string     // Concatenated deny/halt reasons (newline-separated).
	Context       string     // Concatenated context from all hooks.
	UpdatedInput  string     // Merged tool_input JSON (empty if no patches).
	UpdatedPrompt string     // Last non-empty updated_prompt in config order.
}

// aggregate merges multiple HookResults into a single AggregateResult.
// Results are processed in config order (the order of the slice). Deny
// wins over allow, allow wins over none. Halt is sticky. Reasons and
// context concatenate in order. updated_input patches shallow-merge in
// order against the original tool input; later patches override earlier
// ones on colliding keys.
func aggregate(event string, dispatched []DispatchResult, origToolInput string) AggregateResult {
	var (
		decision      Decision
		halt          bool
		reasons       []string
		contexts      []string
		merged        = origToolInput
		anyPatch      = false
		updatedPrompt string
	)
	infos := make([]HookInfo, len(dispatched))
	for i, d := range dispatched {
		r := d.Result
		if event == EventPreToolUse && r.UpdatedInput != "" {
			next, err := shallowMerge(merged, r.UpdatedInput)
			if err != nil {
				slog.Warn(
					"Hook updated_input patch rejected; denying tool call",
					"hook", d.Name,
					"error", err,
				)
				r.Decision = DecisionDeny
				r.Reason = "hook input rewrite rejected: " + err.Error()
				r.UpdatedInput = ""
			} else {
				merged = next
				anyPatch = true
			}
		}
		infos[i] = DispatchResult{Name: d.Name, Matcher: d.Matcher, Result: r}.info()
		switch r.Decision {
		case DecisionDeny:
			decision = DecisionDeny
			if r.Reason != "" {
				reasons = append(reasons, r.Reason)
			}
		case DecisionAllow:
			if decision != DecisionDeny {
				decision = DecisionAllow
			}
		case DecisionNone, DecisionInvalid:
			// No change.
		}
		if r.Halt {
			halt = true
			if r.Reason != "" && r.Decision != DecisionDeny {
				// A halting hook that didn't also deny still contributes
				// its reason so the user sees it.
				reasons = append(reasons, r.Reason)
			}
		}
		if r.Context != "" {
			contexts = append(contexts, r.Context)
		}
		// A prompt is a single string with no key structure to merge
		// against, so updated_prompt is a full replacement: later hooks
		// in config order win.
		if r.UpdatedPrompt != "" {
			updatedPrompt = r.UpdatedPrompt
		}
	}
	agg := AggregateResult{
		Decision:      decision,
		Halt:          halt,
		HookCount:     len(dispatched),
		Hooks:         infos,
		UpdatedPrompt: updatedPrompt,
	}
	if anyPatch {
		agg.UpdatedInput = merged
	}
	if len(reasons) > 0 {
		agg.Reason = strings.Join(reasons, "\n")
	}
	if len(contexts) > 0 {
		agg.Context = strings.Join(contexts, "\n")
	}
	return agg
}

// finalizeAggregation aggregates raw hook results into the
// AggregateResult every entry point returns and logs the completion
// line. It returns the decision-none aggregate when no hook ran, so
// neither the aggregation nor the log line fires for an empty run.
func finalizeAggregation(ec EventContext, dispatched []DispatchResult) (AggregateResult, error) {
	if len(dispatched) == 0 {
		return AggregateResult{Decision: DecisionNone}, nil
	}
	agg := aggregate(ec.Event, dispatched, ec.ToolInput)
	slog.Info(
		"Hook completed",
		"event", ec.Event,
		"tool", ec.ToolName,
		"hooks", len(dispatched),
		"decision", agg.Decision.String(),
	)
	return agg, nil
}

// shallowMerge applies a top-level-keys patch to base (both JSON
// objects). Keys in patch overwrite keys in base; keys absent from the
// patch are preserved. Returns an error if either value is not a valid
// JSON object.
func shallowMerge(base, patch string) (string, error) {
	if base == "" {
		base = "{}"
	}
	baseMap, err := decodeObject(base, "tool_input")
	if err != nil {
		return "", err
	}
	patchMap, err := decodeObject(patch, "updated_input")
	if err != nil {
		return "", err
	}
	maps.Copy(baseMap, patchMap)
	out, err := json.Marshal(baseMap)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func decodeObject(data, name string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &m); err != nil || m == nil {
		return nil, errNotObject(name)
	}
	return m, nil
}

type errNotObject string

func (e errNotObject) Error() string { return string(e) + " is not a JSON object" }

// Dispatcher runs in-process hook handlers for an event, alongside the
// shell commands the config declares. It is what lets Lua extensions
// answer the same events without spawning a process.
//
// A dispatcher that does not handle an event returns false from Has, so
// the registry can skip building a payload for it. The payload is built
// once by the caller (Registry.Run) and handed to every consumer of the
// event - shell hooks and dispatchers alike - so an event with both is
// marshalled once, not once per source.
type Dispatcher interface {
	Has(event string) bool
	Dispatch(ctx context.Context, ec EventContext, payload []byte) []DispatchResult
}

// DispatchResult is one in-process handler's outcome, named for the
// hook indicator the UI draws.
type DispatchResult struct {
	Name    string
	Matcher string
	Result  HookResult
}

// info renders a dispatch result the way runner results are rendered.
func (d DispatchResult) info() HookInfo {
	return HookInfo{
		Name:         d.Name,
		Matcher:      d.Matcher,
		Decision:     d.Result.Decision.String(),
		Halt:         d.Result.Halt,
		Reason:       d.Result.Reason,
		InputRewrite: d.Result.UpdatedInput != "",
	}
}
