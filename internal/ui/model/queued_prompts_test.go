package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/chat"
)

// newQueuedTestUI wires a UI whose workspace reports busy so sends queue.
func newQueuedTestUI() (*UI, *countingWorkspace) {
	ws := &countingWorkspace{ready: true, agentBusy: true}
	m := newBusyUI(ws)
	warmCaches(m, true)
	return m, ws
}

// queuedPlaceholderIDs returns the IDs of the transcript's queued-prompt
// placeholders, in order.
func (m *UI) queuedPlaceholderIDs() []string {
	ids := make([]string, len(m.queuedPromptsShown))
	for i, item := range m.queuedPromptsShown {
		ids[i] = item.ID()
	}
	return ids
}

// TestQueuedPromptRendersImmediately pins the optimistic half: a prompt
// submitted while the agent is busy lands in the transcript right away,
// marked queued, before any authoritative queue refresh runs.
func TestQueuedPromptRendersImmediately(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	require.False(t, m.chat.MessageItem("queued-prompt-1") != nil)
	_ = m.sendMessage("steer this way")

	require.Len(t, m.queuedPromptsShown, 1, "the prompt must appear in the transcript")
	item := m.chat.MessageItem("queued-prompt-1")
	require.NotNil(t, item, "the placeholder must be in the chat list")
	queued, ok := item.(*chat.QueuedMessageItem)
	require.True(t, ok)
	assert.Equal(t, "steer this way", queued.Text())
	assert.Contains(t, item.Render(100), "queued", "the entry is marked as queued")
}

// TestQueuedPromptMaterializesIntoRealMessage pins the swap: when the
// agent dequeues the prompt and the real user message lands, the
// placeholder drops in the same pass so the transcript shows the message
// exactly once.
func TestQueuedPromptMaterializesIntoRealMessage(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendMessage("steer this way")
	require.Len(t, m.queuedPromptsShown, 1)

	m.appendSessionMessage(message.Message{
		ID:        "real-1",
		SessionID: "s1",
		Role:      message.User,
		Parts:     []message.ContentPart{message.TextContent{Text: "steer this way"}},
	})

	assert.Empty(t, m.queuedPromptsShown, "the placeholder must be dropped")
	assert.Nil(t, m.chat.MessageItem("queued-prompt-1"), "the placeholder leaves the list")
	assert.NotNil(t, m.chat.MessageItem("real-1"), "the real message replaces it")
}

// TestQueuedPromptReconcileWithAuthoritativeQueue pins the reconcile
// half: entries that left the queue drop out, and entries queued from
// elsewhere appear.
func TestQueuedPromptReconcileWithAuthoritativeQueue(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendMessage("mine")

	// The authoritative fetch confirms the local entry and adds one
	// queued from another client.
	m.reconcileQueuedPrompts([]string{"mine", "from another client"})
	require.Len(t, m.queuedPromptsShown, 2)
	assert.Equal(t, []string{"queued-prompt-1", "queued-prompt-2"}, m.queuedPlaceholderIDs())

	// The queue drains: both leave without user messages (e.g. cleared),
	// so both placeholders drop.
	m.reconcileQueuedPrompts(nil)
	assert.Empty(t, m.queuedPromptsShown)
	assert.Nil(t, m.chat.MessageItem("queued-prompt-1"))
	assert.Nil(t, m.chat.MessageItem("queued-prompt-2"))
}

// TestQueuedPromptReconcileKeepsUnmatchedLocalSends pins that a
// placeholder whose prompt is queued twice (same text, two entries) is
// matched one-for-one rather than both collapsing onto one queue entry.
func TestQueuedPromptReconcileKeepsUnmatchedLocalSends(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendMessage("again")
	_ = m.sendMessage("again")
	require.Len(t, m.queuedPromptsShown, 2)

	m.reconcileQueuedPrompts([]string{"again", "again"})
	assert.Len(t, m.queuedPromptsShown, 2, "two identical queued prompts keep two entries")
}
