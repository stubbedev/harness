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

func withRuntimeContext(messages []fantasy.Message, runtime string) []fantasy.Message {
	if runtime == "" {
		return messages
	}
	return append(messages, fantasy.NewUserMessage("<harness_runtime>\n"+runtime+"\n</harness_runtime>"))
}
