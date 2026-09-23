package config

import "strings"

// Hook event names. This is the one list of events: the hooks package
// re-exports these, and config validation accepts exactly these.
const (
	// HookPreToolUse fires before a tool call runs.
	HookPreToolUse = "PreToolUse"
	// HookPostToolUse fires after a tool call completes. The payload
	// carries the tool response in addition to the input.
	HookPostToolUse = "PostToolUse"
	// HookUserPromptSubmit fires after the user submits a prompt but
	// before it reaches the model. Can block, rewrite, or annotate the
	// prompt.
	HookUserPromptSubmit = "UserPromptSubmit"
	// HookSessionStart fires on the first prompt of a session.
	HookSessionStart = "SessionStart"
	// HookStop fires when the top-level agent finishes a turn.
	HookStop = "Stop"
	// HookSubagentStop fires when a dispatched sub-agent finishes.
	HookSubagentStop = "SubagentStop"
	// HookNotification fires when Harness sends a user notification
	// (agent finished, agent error, provider retry).
	HookNotification = "Notification"
	// HookPreCompact fires before a session is summarized/compacted.
	HookPreCompact = "PreCompact"
	// HookPostCompact fires after a session was summarized/compacted.
	HookPostCompact = "PostCompact"
)

// HookEvents lists every hook event this build understands, in a stable
// order.
func HookEvents() []string {
	return []string{
		HookPreToolUse,
		HookPostToolUse,
		HookUserPromptSubmit,
		HookSessionStart,
		HookStop,
		HookSubagentStop,
		HookNotification,
		HookPreCompact,
		HookPostCompact,
	}
}

// CanonicalHookEvent maps a user-provided event name to its canonical
// form and reports whether it names a known event. Matching ignores case
// and underscores, so "pre_tool_use" and "pretooluse" both yield
// "PreToolUse".
func CanonicalHookEvent(name string) (string, bool) {
	folded := strings.ReplaceAll(name, "_", "")
	for _, event := range HookEvents() {
		if strings.EqualFold(folded, event) {
			return event, true
		}
	}
	return name, false
}
