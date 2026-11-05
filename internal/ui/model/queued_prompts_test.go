package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/chat"
	"github.com/stubbedev/harness/internal/ui/styles"
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
	_ = m.sendReadyMessage("steer this way")

	require.Len(t, m.queuedPrompts, 1, "the prompt must appear in the transcript")
	item := m.chat.MessageItem("queued-prompt-1")
	require.NotNil(t, item, "the placeholder must be in the chat list")
	queued, ok := item.(*chat.QueuedMessageItem)
	require.True(t, ok)
	assert.Equal(t, "steer this way", queued.Text())
	assert.Contains(t, item.Render(100), styles.QueuedIcon, "the entry is marked as queued")
}

// TestQueuedPromptsJoinIntoSingleEntry pins the join: a second prompt
// queued behind the first extends the same transcript entry instead of
// adding another, and its text is the join of both prompts.
func TestQueuedPromptsJoinIntoSingleEntry(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendReadyMessage("first")
	_ = m.sendReadyMessage("second")

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

	_ = m.sendReadyMessage("steer this way")
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

	_ = m.sendReadyMessage("one")
	_ = m.sendReadyMessage("two")
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

	_ = m.sendReadyMessage("mine")

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

	_ = m.sendReadyMessage("again")
	_ = m.sendReadyMessage("again")
	require.Len(t, m.queuedPrompts, 2)

	m.reconcileQueuedPrompts([]string{"again", "again"})
	assert.Len(t, m.queuedPrompts, 2, "two identical queued prompts keep two entries")
	item := m.chat.MessageItem("queued-prompt-1")
	require.NotNil(t, item)
	assert.Equal(t,
		"again"+message.QueuedPromptSeparator+"again",
		item.(*chat.QueuedMessageItem).Text())
}

const queuedTestChild = "m1$$c1"

func showAgentView(m *UI, childSessionID string) {
	m.agentView.requested = childSessionID
	_ = m.handleAgentTranscriptMsg(agentTranscriptMsg{
		forSession:     m.session.ID,
		childSessionID: childSessionID,
		msgs: []message.Message{{
			ID:        "child-prompt",
			SessionID: childSessionID,
			Role:      message.User,
			Parts:     []message.ContentPart{message.TextContent{Text: "child prompt"}},
		}},
	})
}

func showMainView(m *UI, msgs ...message.Message) {
	m.agentView.requested = ""
	_ = m.handleAgentTranscriptMsg(agentTranscriptMsg{forSession: m.session.ID, msgs: msgs})
}

func hasQueuedPlaceholder(m *UI) bool {
	return m.queuedPromptItem != nil && m.chat.MessageItem(m.queuedPromptItem.ID()) != nil
}

// TestQueuedPromptNeverPaintsIntoAgentView pins that the main session's
// queued prompt is drawn only in the main transcript: a queue refresh
// arriving while an agent's session is on screen must not add the
// placeholder to the agent's transcript.
func TestQueuedPromptNeverPaintsIntoAgentView(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendReadyMessage("queued for main")
	showAgentView(m, queuedTestChild)
	showMainView(m)
	showAgentView(m, queuedTestChild)

	m.reconcileQueuedPrompts([]string{"queued for main", "from another client"})

	assert.False(t, hasQueuedPlaceholder(m), "the agent transcript must not show the main session's queue")
	assert.NotNil(t, m.chat.MessageItem("child-prompt"), "the agent transcript is on screen")
	assert.Len(t, m.queuedPrompts, 2, "the queue is still tracked for the main transcript")
}

// TestQueuedPromptReturnsWithMainTranscript pins that leaving for an
// agent's session and coming back keeps a still-queued prompt in the
// main transcript instead of dropping it until the next refresh.
func TestQueuedPromptReturnsWithMainTranscript(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendReadyMessage("queued for main")
	showAgentView(m, queuedTestChild)
	showMainView(m)

	require.True(t, hasQueuedPlaceholder(m), "the queued prompt comes back with the main transcript")
	assert.Equal(t, "queued for main", m.queuedPromptItem.Text())
}

// TestQueuedPromptDrainedWhileAgentViewedIsNotRestored pins that a
// queued prompt which became a real message while an agent was on
// screen is consumed, so returning to main shows the message once and
// no stale placeholder.
func TestQueuedPromptDrainedWhileAgentViewedIsNotRestored(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendReadyMessage("queued for main")
	showAgentView(m, queuedTestChild)

	drained := message.Message{
		ID:        "real-1",
		SessionID: "s1",
		Role:      message.User,
		Parts:     []message.ContentPart{message.TextContent{Text: "queued for main"}},
	}
	_, _ = m.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: drained})
	assert.Empty(t, m.queuedPrompts, "the drained prompt is consumed even off screen")

	showMainView(m, drained)
	assert.False(t, hasQueuedPlaceholder(m), "no stale placeholder next to the real message")
	assert.NotNil(t, m.chat.MessageItem("real-1"))
}

// TestQueuedPromptsDroppedOnSessionSwitch pins that another session's
// transcript never inherits the queue of the session left behind.
func TestQueuedPromptsDroppedOnSessionSwitch(t *testing.T) {
	pinTTLs(t)
	m, _ := newQueuedTestUI()

	_ = m.sendReadyMessage("queued for s1")
	m.session = &session.Session{ID: "s2"}
	_ = m.setSessionMessages(nil)

	assert.Empty(t, m.queuedPrompts)
	assert.False(t, hasQueuedPlaceholder(m))
}
