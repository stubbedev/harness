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

// TestQueuedPromptRendersImmediately pins the optimistic half: a prompt
// submitted while the agent is busy lands in the transcript right away,
// marked queued, before any authoritative queue refresh runs.
func TestQueuedPromptRendersImmediately(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	require.Nil(t, m.chat.MessageItem("queued-prompt-1"))
	_ = m.sendMessage("steer this way")

	require.Len(t, m.queuedPrompts, 1, "the prompt must appear in the transcript")
	item := m.chat.MessageItem("queued-prompt-1")
	require.NotNil(t, item, "the placeholder must be in the chat list")
	queued, ok := item.(*chat.QueuedMessageItem)
	require.True(t, ok)
	assert.Equal(t, "steer this way", queued.Text())
	assert.Contains(t, item.Render(100), "queued", "the entry is marked as queued")
}

// TestQueuedPromptsJoinIntoSingleEntry pins the join: a second prompt
// queued behind the first extends the same transcript entry instead of
// adding another, and its text is the join of both prompts.
func TestQueuedPromptsJoinIntoSingleEntry(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendMessage("first")
	_ = m.sendMessage("second")

	require.Len(t, m.queuedPrompts, 2)
	item := m.chat.MessageItem("queued-prompt-1")
	require.NotNil(t, item, "both prompts share the first placeholder")
	require.Nil(t, m.chat.MessageItem("queued-prompt-2"), "no second placeholder is added")
	joined := "first" + message.QueuedPromptSeparator + "second"
	assert.Equal(t, joined, item.(*chat.QueuedMessageItem).Text())
	assert.Contains(t, item.Render(100), "second", "the joined entry shows both prompts")
}

// TestQueuedPromptMaterializesIntoRealMessage pins the swap: when the
// agent dequeues the prompts and the real joined user message lands, the
// placeholder drops in the same pass so the transcript shows the message
// exactly once.
func TestQueuedPromptMaterializesIntoRealMessage(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendMessage("steer this way")
	require.Len(t, m.queuedPrompts, 1)

	m.appendSessionMessage(message.Message{
		ID:        "real-1",
		SessionID: "s1",
		Role:      message.User,
		Parts:     []message.ContentPart{message.TextContent{Text: "steer this way"}},
	})

	assert.Empty(t, m.queuedPrompts, "the placeholder must be dropped")
	assert.Nil(t, m.chat.MessageItem("queued-prompt-1"), "the placeholder leaves the list")
	assert.NotNil(t, m.chat.MessageItem("real-1"), "the real message replaces it")
}

// TestJoinedQueuedPromptsMaterializeTogether pins that the joined
// message the agent creates when the queue drains consumes the whole
// placeholder at once.
func TestJoinedQueuedPromptsMaterializeTogether(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendMessage("one")
	_ = m.sendMessage("two")
	require.Len(t, m.queuedPrompts, 2)

	m.appendSessionMessage(message.Message{
		ID:        "real-joined",
		SessionID: "s1",
		Role:      message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "one" + message.QueuedPromptSeparator + "two"},
		},
	})

	assert.Empty(t, m.queuedPrompts, "the joined message consumes every queued prompt")
	assert.Nil(t, m.chat.MessageItem("queued-prompt-1"), "the placeholder leaves the list")
	assert.NotNil(t, m.chat.MessageItem("real-joined"))
}

// TestQueuedPromptReconcileWithAuthoritativeQueue pins the reconcile
// half: prompts that left the queue drop out, and prompts queued from
// elsewhere join the entry.
func TestQueuedPromptReconcileWithAuthoritativeQueue(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendMessage("mine")

	// The authoritative fetch confirms the local entry and adds one
	// queued from another client.
	m.reconcileQueuedPrompts([]string{"mine", "from another client"})
	require.Len(t, m.queuedPrompts, 2)
	item := m.chat.MessageItem("queued-prompt-1")
	require.NotNil(t, item, "reconcile keeps the single placeholder")
	assert.Equal(t,
		"mine"+message.QueuedPromptSeparator+"from another client",
		item.(*chat.QueuedMessageItem).Text())

	// The queue drains: both leave without user messages (e.g. cleared),
	// so the placeholder drops.
	m.reconcileQueuedPrompts(nil)
	assert.Empty(t, m.queuedPrompts)
	assert.Nil(t, m.chat.MessageItem("queued-prompt-1"))
}

// TestQueuedPromptReconcileKeepsDuplicates pins that a prompt queued
// twice (same text, two entries) keeps both entries in the joined
// placeholder rather than collapsing onto one.
func TestQueuedPromptReconcileKeepsDuplicates(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendMessage("again")
	_ = m.sendMessage("again")
	require.Len(t, m.queuedPrompts, 2)

	m.reconcileQueuedPrompts([]string{"again", "again"})
	assert.Len(t, m.queuedPrompts, 2, "two identical queued prompts keep two entries")
	item := m.chat.MessageItem("queued-prompt-1")
	require.NotNil(t, item)
	assert.Equal(t,
		"again"+message.QueuedPromptSeparator+"again",
		item.(*chat.QueuedMessageItem).Text())
}
