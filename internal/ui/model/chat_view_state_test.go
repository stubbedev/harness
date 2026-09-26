package model

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/chat"
)

// viewStateSession builds a session transcript: a prompt, a run of two
// tool calls, then enough follow-ups to overflow the viewport, so
// expansion, selection and scroll each have somewhere to be.
func viewStateSession(sessionID, prefix string) []message.Message {
	msgs := []message.Message{
		{ID: prefix + "u", SessionID: sessionID, Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "the ask"},
		}},
		{ID: prefix + "calls", SessionID: sessionID, Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: prefix + "t1", Name: "shell", Input: `{"command":"ls"}`, Finished: true},
			message.ToolCall{ID: prefix + "t2", Name: "shell", Input: `{"command":"pwd"}`, Finished: true},
		}},
		{ID: prefix + "results", SessionID: sessionID, Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: prefix + "t1", Name: "shell", Content: "a\nb\nc"},
			message.ToolResult{ToolCallID: prefix + "t2", Name: "shell", Content: "/tmp"},
		}},
	}
	for i := range 40 {
		msgs = append(msgs, message.Message{
			ID: fmt.Sprintf("%sr%d", prefix, i), SessionID: sessionID, Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: fmt.Sprintf("follow-up %d", i)},
			},
		})
	}
	return msgs
}

// arrangeView expands the transcript's tool run, parks the sub-cursor on
// its second call with that call opened, and scrolls to the top: a view
// no fresh transcript opens on.
func arrangeView(t *testing.T, u *UI, groupID string) {
	t.Helper()
	idx, ok := u.chat.itemIndex(groupID)
	require.True(t, ok)
	u.chat.SetSelected(idx)
	u.chat.ToggleExpandedSelectedItem()
	g, ok := u.chat.selectedGroup()
	require.True(t, ok)
	g.SetSelectedChild(1)
	require.True(t, g.ToggleSelectedChild())
	u.chat.ScrollToTop()
}

// requireArrangedView asserts the view arrangeView left.
func requireArrangedView(t *testing.T, u *UI, groupID, childID string, want ChatViewState) {
	t.Helper()
	g, ok := u.chat.selectedGroup()
	require.True(t, ok, "the tool run is selected again")
	require.Equal(t, groupID, g.ID())
	require.True(t, g.ExpandedLevel(), "the tool run is expanded again")
	require.Equal(t, 1, g.SelectedChild(), "the sub-cursor is on the same call")
	require.NotZero(t, g.ChildTool(childID).ExpansionLevel(), "the opened call is open again")
	offsetIdx, offsetLine := u.chat.ScrollPosition()
	gotID := u.chat.list.ItemAt(offsetIdx).(chat.MessageItem).ID()
	require.Equal(t, want.offsetID, gotID, "the scroll anchor is the same item")
	require.Equal(t, want.offsetLine, offsetLine, "the scroll anchor is the same line")
	require.False(t, u.chat.Follow())
}

// TestViewSwitchKeepsViewState pins issue #71: switching the transcript
// between the main session and an agent's, in either direction, brings
// each back exactly as the user left it - what was expanded, where the
// selection and sub-cursor sat, and the scroll position - even though
// every switch rebuilds the items from messages.
func TestViewSwitchKeepsViewState(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	ws := &testWorkspace{cfg: &config.Config{}}
	u.com.Workspace = ws
	u.session = &session.Session{ID: "s1"}
	const child = "agent-tool-m1-a1"
	ws.messages = map[string][]message.Message{
		"s1":  viewStateSession("s1", "main-"),
		child: viewStateSession(child, "agent-"),
	}
	dispatch := func() {
		_ = u.upsertAgentTask(&message.Message{ID: "m1", Role: message.Assistant}, agentToolCall("a1"))
	}
	toMain := func() {
		u.focusTasks()
		u.taskCursor = 0
		cmd := u.activateTaskAtCursor()
		require.NotNil(t, cmd)
		applyAgentTranscript(t, u, cmd())
	}

	_ = u.setSessionMessages(ws.messages["s1"])
	u.updateLayoutAndSize()
	arrangeView(t, u, "toolgroup-main-t1")
	mainView := u.chat.captureViewState()

	// Into the agent: a transcript never shown opens at its default.
	dispatch()
	activateAgentView(t, u)
	require.True(t, u.chat.Follow(), "a first visit opens at the bottom")
	arrangeView(t, u, "toolgroup-agent-t1")
	agentView := u.chat.captureViewState()

	// Back to main: exactly as left.
	toMain()
	requireArrangedView(t, u, "toolgroup-main-t1", "main-t2", mainView)

	// And into the agent again: its own state, not main's.
	dispatch()
	activateAgentView(t, u)
	requireArrangedView(t, u, "toolgroup-agent-t1", "agent-t2", agentView)
}

// TestViewStateKeepsNewItemsDefault pins that a restore only touches the
// items it captured: one that arrived while the view was away keeps the
// shape it was built with, and a view left following returns to the
// bottom, where the new item is.
func TestViewStateKeepsNewItemsDefault(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}
	u.session = &session.Session{ID: "s1"}
	msgs := viewStateSession("s1", "")
	_ = u.setSessionMessages(msgs)
	u.updateLayoutAndSize()
	require.True(t, u.chat.Follow())

	u.chat.ClearMessages()
	msgs = append(msgs,
		message.Message{ID: "late", SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "late-t", Name: "shell", Input: `{"command":"true"}`, Finished: true},
		}},
		message.Message{ID: "late-res", SessionID: "s1", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "late-t", Name: "shell", Content: "done"},
		}},
	)
	_ = u.setSessionMessages(msgs)
	require.True(t, u.chat.Follow(), "a view left following returns to the bottom")
	require.True(t, u.chat.AtBottom())
	require.Zero(t, u.chat.ToolItem("late-t").ExpansionLevel(), "a new call keeps its default")
}
