package chat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// versionedItem is the cross-cutting interface every chat item type
// must satisfy under F6: every documented mutator must bump the
// shared version counter so the list-level memo invalidates.
type versionedItem interface {
	list.Item
	Version() uint64
}

// requireBump asserts that the supplied mutator advances the item's
// Version(). The mutator runs once; an absent bump is a regression
// (a finished item would keep serving stale frozen output to the
// list cache).
func requireBump(t *testing.T, name string, item versionedItem, mutate func()) {
	t.Helper()
	before := item.Version()
	mutate()
	after := item.Version()
	require.Greaterf(t, after, before, "%s must bump Version() (before=%d, after=%d)", name, before, after)
}

// pulseTestFrames is enough clock frames to cross at least one pulse
// glyph change: the minimal spinner only changes its glyph every few
// frames, so Advance bumps the version on those frames alone.
const pulseTestFrames = 10

// TestAssistantMessageItem_MutatorsBumpVersion enumerates every
// documented mutator on AssistantMessageItem and asserts each one
// advances Version().
func TestAssistantMessageItem_MutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	build := func(thinking, content string) *message.Message {
		parts := []message.ContentPart{
			message.ReasoningContent{
				Thinking:   thinking,
				StartedAt:  testStartedAt,
				FinishedAt: testFinishedAt,
			},
		}
		if content != "" {
			parts = append(parts, message.TextContent{Text: content})
		}
		return &message.Message{ID: "a-mut", Role: message.Assistant, Parts: parts}
	}

	item := NewAssistantMessageItem(&sty, build("thinking", "content")).(*AssistantMessageItem)

	requireBump(t, "SetMessage", item, func() {
		item.SetMessage(build("thinking", "more content"))
	})
	requireBump(t, "SetFocused", item, func() {
		item.SetFocused(true)
	})
	requireBump(t, "SetHighlight", item, func() {
		item.SetHighlight(0, 0, 0, 5)
	})
	// ToggleExpanded only mutates state when there is non-empty
	// thinking text — which the build helper provides.
	requireBump(t, "ToggleExpanded", item, func() {
		item.ToggleExpanded()
	})
}

// TestUserMessageItem_MutatorsBumpVersion enumerates UserMessageItem
// mutators.
func TestUserMessageItem_MutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "u-mut",
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "Hello"},
		},
	}
	item := NewUserMessageItem(&sty, msg).(*UserMessageItem)

	requireBump(t, "SetFocused", item, func() {
		item.SetFocused(true)
	})
	requireBump(t, "SetHighlight", item, func() {
		item.SetHighlight(0, 0, 0, 3)
	})
}

// TestAssistantInfoItem_VersionedAndFinished sanity-checks the
// AssistantInfoItem wiring. The item carries only immutable data
// after construction; we still assert Version() is callable and
// Finished() returns true.
func TestAssistantInfoItem_VersionedAndFinished(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	cfg := &config.Config{}
	msg := &message.Message{
		ID:    "info",
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.Finish{Reason: message.FinishReasonEndTurn, Time: time.Now().Unix()}},
	}
	item := NewAssistantInfoItem(&sty, msg, cfg, time.Unix(0, 0)).(*AssistantInfoItem)

	require.True(t, item.Finished(), "AssistantInfoItem must be Finished()")
	// Version() is callable and starts at zero.
	require.Equal(t, uint64(0), item.Version())
}

// TestBaseToolMessageItem_MutatorsBumpVersion enumerates the base
// tool item mutators. Specific tool types layer on top of this
// base; the base bumps cover the shared mutator surface.
func TestBaseToolMessageItem_MutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{ID: "tc1", Name: "shell", Input: "{}", Finished: false}
	item := NewToolMessageItem(&sty, "msg", tc, nil, false, "")

	v := item.(versionedItem)

	requireBump(t, "SetFocused", v, func() {
		if f, ok := item.(list.Focusable); ok {
			f.SetFocused(true)
		}
	})
	requireBump(t, "SetHighlight", v, func() {
		if h, ok := item.(list.Highlightable); ok {
			h.SetHighlight(0, 0, 0, 3)
		}
	})
	requireBump(t, "SetToolCall", v, func() {
		tc2 := tc
		tc2.Input = `{"command":"echo"}`
		item.SetToolCall(tc2)
	})
	requireBump(t, "SetResult", v, func() {
		item.SetResult(&message.ToolResult{ToolCallID: "tc1", Content: "ok"})
	})
	requireBump(t, "ToggleExpanded", v, func() {
		if e, ok := item.(Expandable); ok {
			e.ToggleExpanded()
		}
	})
	requireBump(t, "SetCompact", v, func() {
		if c, ok := item.(Compactable); ok {
			c.SetCompact(true)
		}
	})
}

// TestAssistantMessageItem_AdvanceBumpsVersion covers the spinner
// regression: while the assistant message is spinning, clock frames
// fed through Advance must bump Version() on every glyph change so
// the list-level cache invalidates and the next draw re-renders the
// advanced spinner frame. Without this bump the cached entry's
// version stays put and the spinner appears frozen.
func TestAssistantMessageItem_AdvanceBumpsVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	streaming := &message.Message{
		ID:   "spin",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "thinking..."},
		},
	}
	item := NewAssistantMessageItem(&sty, streaming).(*AssistantMessageItem)
	require.True(t, item.Spinning())

	requireBump(t, "Advance", item, func() {
		for range pulseTestFrames {
			item.Advance()
		}
	})

	// A non-spinning item must not bump on Advance: the bump only
	// makes sense while the spinner is live, and a stray bump on a
	// finished item would needlessly invalidate frozen entries.
	finished := &message.Message{
		ID:   "spin",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "done"},
			message.Finish{Reason: message.FinishReasonEndTurn, Time: testFinishTime},
		},
	}
	item.SetMessage(finished)
	require.True(t, item.Finished(), "item must report Finished() once the message finishes")
	require.False(t, item.Spinning())
	before := item.Version()
	item.Advance()
	require.Equal(t, before, item.Version(), "Advance must not bump Version() on a non-spinning item")
}

// TestAssistantMessageItem_FinishedTransition covers §4.5.1: a
// streaming assistant message reports Finished() == false; once the
// message reports IsFinished() and stops spinning, Finished() must
// return true.
func TestAssistantMessageItem_FinishedTransition(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	// Streaming: no finish part, no content yet — isSpinning == true.
	streaming := &message.Message{
		ID:   "stream",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "thinking..."},
		},
	}
	item := NewAssistantMessageItem(&sty, streaming).(*AssistantMessageItem)
	require.False(t, item.Finished(), "streaming assistant message must not be Finished()")

	// Finished with content.
	finished := &message.Message{
		ID:   "stream",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "thinking", StartedAt: testStartedAt, FinishedAt: testFinishedAt},
			message.TextContent{Text: "the answer"},
			message.Finish{Reason: message.FinishReasonEndTurn, Time: testFinishTime},
		},
	}
	item.SetMessage(finished)
	require.True(t, item.Finished(), "finished assistant message must be Finished()")
}

// TestUserMessageItem_FinishedAlwaysTrue locks in the freezable
// contract: user messages are never spinning.
func TestUserMessageItem_FinishedAlwaysTrue(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:    "u-fin",
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hi"}},
	}
	item := NewUserMessageItem(&sty, msg).(*UserMessageItem)
	require.True(t, item.Finished())
}

// requireNoBump asserts the supplied mutator leaves the item's
// Version() unchanged.
func requireNoBump(t *testing.T, name string, item versionedItem, mutate func()) {
	t.Helper()
	before := item.Version()
	mutate()
	after := item.Version()
	require.Equalf(t, before, after,
		"%s must not bump Version() (before=%d, after=%d)", name, before, after)
}

// TestBaseToolMessageItem_AdvanceBumpsVersion is the spinner
// regression test for non-agent tools: while the tool is spinning,
// clock frames must bump Version() on every glyph change so the
// list-level cache invalidates and the next draw re-renders the
// advanced spinner frame. A finished tool must not bump (the entry is
// frozen and stays frozen) and must report that it no longer needs
// frames.
func TestBaseToolMessageItem_AdvanceBumpsVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{ID: "tc-spin", Name: "shell", Input: "{}", Finished: false}
	item := NewToolMessageItem(&sty, "msg", tc, nil, false, "")
	v := item.(versionedItem)
	a, ok := item.(Animatable)
	require.True(t, ok, "base tool message item must implement Animatable")
	require.True(t, a.Spinning())

	requireBump(t, "Advance[spinning]", v, func() {
		for range pulseTestFrames {
			a.Advance()
		}
	})

	// Finished → no bump. The entry is frozen; a stray bump would
	// needlessly invalidate frozen entries.
	tcFinished := tc
	tcFinished.Finished = true
	item.SetToolCall(tcFinished)
	item.SetResult(&message.ToolResult{ToolCallID: tc.ID, Content: "ok"})
	require.True(t, item.Finished(), "tool must report Finished() once the result lands")
	require.False(t, a.Spinning(), "finished tool must not request frames")

	requireNoBump(t, "Advance[finished]", v, func() {
		a.Advance()
	})
}

// TestBaseToolMessageItem_FinishedTransition covers §4.5.1 for
// tools: a still-running tool reports Finished() == false; once the
// tool call is marked finished and a result lands, Finished()
// returns true. Cancelled tools also become Finished.
func TestBaseToolMessageItem_FinishedTransition(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{ID: "tc-fin", Name: "shell", Input: "{}", Finished: false}
	item := NewToolMessageItem(&sty, "msg", tc, nil, false, "")
	require.False(t, item.Finished(), "running tool must not be Finished()")

	tcFinished := tc
	tcFinished.Finished = true
	item.SetToolCall(tcFinished)
	item.SetResult(&message.ToolResult{ToolCallID: "tc-fin", Content: "ok"})
	require.True(t, item.Finished(), "finished tool with result must be Finished()")

	// Canceled tool with no result is also Finished.
	tcCanceled := message.ToolCall{ID: "tc-cancel", Name: "shell", Input: "{}", Finished: false}
	canceled := NewToolMessageItem(&sty, "msg", tcCanceled, nil, true, "")
	require.True(t, canceled.Finished(), "canceled tool must be Finished()")
}
