package agent

import (
	"slices"
	"strings"

	"charm.land/fantasy"
)

// turnInjections holds what a turn added to its requests beyond the
// messages fantasy carries from step to step.
//
// fantasy rebuilds every step's input from the initial prompt plus the
// assistant and tool messages it produced itself, so anything PrepareStep
// appended - a folded follow-up prompt, a sub-agent report-back, a
// context note - is gone by the next step unless it is put back. Putting
// it back at the end would move it behind the step's new tool results,
// and a message that moves is a request prefix that changes, which is
// what prompt caching cannot survive. Each entry therefore remembers the
// index in fantasy's messages it was appended at and goes back in front
// of that index on every later step, so the request grows only at its
// end. The rows for these messages are persisted as well, which gives
// the next turn's history the same messages in the same places.
type turnInjections struct {
	entries []turnInjection
}

type turnInjection struct {
	// at is the index in fantasy's step messages the message precedes;
	// it was appended when those messages were at exactly this length.
	at  int
	msg fantasy.Message
}

// add records msgs as appended after the first at messages of the step
// input. Steps only grow, so at never decreases across calls.
func (t *turnInjections) add(at int, msgs ...fantasy.Message) {
	for _, msg := range msgs {
		t.entries = append(t.entries, turnInjection{at: at, msg: msg})
	}
}

// apply returns base with every recorded message put back where it was
// first sent. base is not modified.
func (t *turnInjections) apply(base []fantasy.Message) []fantasy.Message {
	if len(t.entries) == 0 {
		return base
	}
	out := make([]fantasy.Message, 0, len(base)+len(t.entries))
	next := 0
	for i := 0; i <= len(base); i++ {
		for next < len(t.entries) && t.entries[next].at <= i {
			out = append(out, t.entries[next].msg)
			next++
		}
		if i < len(base) {
			out = append(out, base[i])
		}
	}
	for ; next < len(t.entries); next++ {
		out = append(out, t.entries[next].msg)
	}
	return out
}

// messageText concatenates the text parts of a message.
func messageText(msg fantasy.Message) string {
	var sb strings.Builder
	for _, part := range msg.Content {
		if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
			sb.WriteString(text.Text)
		}
	}
	return sb.String()
}

// lastTaggedUserText returns the text of the last user message that
// opens with tag, or "" when there is none.
func lastTaggedUserText(messages []fantasy.Message, tag string) string {
	for _, msg := range slices.Backward(messages) {
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		for _, part := range msg.Content {
			text, ok := fantasy.AsMessagePart[fantasy.TextPart](part)
			if !ok {
				continue
			}
			if strings.HasPrefix(text.Text, tag) {
				return text.Text
			}
			// A note merged behind the prompt it followed.
			if i := strings.Index(text.Text, "\n\n"+tag); i >= 0 {
				return text.Text[i+2:]
			}
		}
	}
	return ""
}

// userTextContains reports whether any user message mentions needle.
func userTextContains(messages []fantasy.Message, needle string) bool {
	for _, msg := range messages {
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && strings.Contains(text.Text, needle) {
				return true
			}
		}
	}
	return false
}
