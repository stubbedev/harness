package model

// Queued-prompt transcript entries.
//
// A prompt submitted while the agent is busy is enqueued server-side and
// does not exist as a message yet. The transcript nevertheless shows it
// the moment it is entered: an optimistic placeholder is appended at
// send time, and the authoritative queue (fetched off-thread, see
// workspace_cache.go) reconciles the set. When the agent dequeues the
// prompt it creates the real user message; the placeholder is dropped in
// the same update pass that appends the real item, so the swap is
// invisible. Prompts queued from another client appear through the same
// reconcile.

import (
	"fmt"
	"slices"

	"github.com/stubbedev/harness/internal/ui/chat"
)

// appendQueuedPrompt adds a transcript placeholder for a prompt that was
// just submitted behind a running turn.
func (m *UI) appendQueuedPrompt(text string) {
	if text == "" || m.chat == nil {
		return
	}
	m.queuedPromptSeq++
	item := chat.NewQueuedMessageItem(m.com.Styles, fmt.Sprintf("queued-prompt-%d", m.queuedPromptSeq), text)
	m.queuedPromptsShown = append(m.queuedPromptsShown, item)
	m.chat.AppendMessages(item)
	m.chat.ScrollToBottom()
}

// materializeQueuedPrompt drops the first placeholder whose text matches
// the user message that just landed in the transcript: the queued prompt
// became a real message.
func (m *UI) materializeQueuedPrompt(text string) {
	if text == "" || m.chat == nil {
		return
	}
	for i, item := range m.queuedPromptsShown {
		if item.Text() == text {
			m.chat.RemoveMessage(item.ID())
			m.queuedPromptsShown = slices.Delete(m.queuedPromptsShown, i, i+1)
			return
		}
	}
}

// reconcileQueuedPrompts aligns the placeholders with the authoritative
// queue list: entries that left the queue without a matching user
// message (cleared, or drained through a path that never publishes one)
// drop out, and entries not yet shown — e.g. queued from another client
// — appear at the end of the transcript.
func (m *UI) reconcileQueuedPrompts(prompts []string) {
	if m.chat == nil {
		return
	}
	remaining := slices.Clone(prompts)
	var kept []*chat.QueuedMessageItem
	for _, item := range m.queuedPromptsShown {
		idx := slices.Index(remaining, item.Text())
		if idx >= 0 {
			kept = append(kept, item)
			remaining = slices.Delete(remaining, idx, idx+1)
			continue
		}
		m.chat.RemoveMessage(item.ID())
	}
	m.queuedPromptsShown = kept
	for _, text := range remaining {
		m.appendQueuedPrompt(text)
	}
}

// resetQueuedPrompts drops placeholder tracking when the transcript is
// rebuilt from persisted messages (session switch or reload): the
// rebuild replaces the list, so the placeholders simply cease to exist.
func (m *UI) resetQueuedPrompts() {
	m.queuedPromptsShown = nil
}
