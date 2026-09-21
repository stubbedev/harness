package agent

import (
	"strings"

	"charm.land/fantasy"
)

func splitRuntimePrompt(rendered string) (string, string) {
	const opening = "\n<harness_runtime>\n"
	const closing = "\n</harness_runtime>"
	start := strings.LastIndex(rendered, opening)
	if start < 0 {
		return rendered, ""
	}
	end := strings.Index(rendered[start+len(opening):], closing)
	if end < 0 || strings.TrimSpace(rendered[start+len(opening)+end+len(closing):]) != "" {
		return rendered, ""
	}
	return rendered[:start], rendered[start+len(opening) : start+len(opening)+end]
}

// withRuntimeContext appends the runtime block as a user message. It is
// written to the session once, behind the first prompt, and found there
// by runtimeContextPresent on every later step and turn; a history that
// was folded into a summary gets a fresh one.
func withRuntimeContext(messages []fantasy.Message, runtime string) []fantasy.Message {
	if runtime == "" {
		return messages
	}
	return append(messages, fantasy.NewUserMessage("<harness_runtime>\n"+runtime+"\n</harness_runtime>"))
}

// runtimeContextPresent reports whether the history already carries a
// runtime block.
func runtimeContextPresent(messages []fantasy.Message) bool {
	return userTextContains(messages, "<harness_runtime>")
}
