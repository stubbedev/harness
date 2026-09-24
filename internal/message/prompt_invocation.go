package message

import (
	"fmt"
	"strconv"
	"strings"
)

// The framing of a named prompt invocation stored as user-message
// text. The head line carries the quoted display name; the element
// body is the expanded prompt text, delivered to the model verbatim.
const (
	promptInvocationHead = "<invoked_prompt name="
	promptInvocationTail = "\n</invoked_prompt>"
)

// FormatPromptInvocation wraps the expanded body of a prompt the user
// invoked by name - a custom command, an MCP prompt or an extension
// command - so the transcript can render it as a compact tool-call
// style row instead of echoing the text as if the user had typed it.
// Like the <loaded_skill> wrapper, the model still receives the full
// body inside the element.
func FormatPromptInvocation(name, body string) string {
	return fmt.Sprintf(
		"%s%q>\n%s%s",
		promptInvocationHead, name, strings.TrimRight(body, "\n"), promptInvocationTail,
	)
}

// ParsePromptInvocation splits message text back into an invoked
// prompt's display name and expanded body. ok is false when the text is
// not a wrapped invocation.
func ParsePromptInvocation(content string) (name, body string, ok bool) {
	head, rest, found := strings.Cut(content, "\n")
	if !found {
		return "", "", false
	}
	quoted, found := strings.CutPrefix(head, promptInvocationHead)
	if !found || !strings.HasSuffix(quoted, ">") {
		return "", "", false
	}
	parsed, err := strconv.Unquote(strings.TrimSuffix(quoted, ">"))
	if err != nil || parsed == "" {
		return "", "", false
	}
	body, found = strings.CutSuffix(rest, promptInvocationTail)
	if !found {
		return "", "", false
	}
	return parsed, body, true
}
