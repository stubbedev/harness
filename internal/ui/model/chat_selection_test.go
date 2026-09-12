package model

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

// streamedTextTurn builds an assistant text-message update like the ones
// the agent publishes while a turn streams: the same message ID, growing
// content.
func streamedTextTurn(id, text string) message.Message {
	return message.Message{
		ID:        id,
		SessionID: "s1",
		Role:      message.Assistant,
		Parts:     []message.ContentPart{message.TextContent{Text: text}},
	}
}

func TestUpdateSessionMessage_ManualSelectionSticks(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.chat.ScrollToBottom()

	// While the selection tracks the newest item, streamed updates keep
	// it selected on the newest item.
	_ = u.appendSessionMessage(streamedTextTurn("a1", "first"))
	_ = u.updateSessionMessage(streamedTextTurn("a1", "first, longer"))
	require.Equal(t, u.chat.Len()-1, u.chat.Selected())
	require.False(t, u.chat.HasManualSelection())

	// A manual selection made mid-stream must survive later updates.
	u.chat.SetSelected(10)
	require.True(t, u.chat.HasManualSelection())
	_ = u.updateSessionMessage(streamedTextTurn("a1", "first, longer still"))
	require.Equal(t, 10, u.chat.Selected(), "streamed updates must not steal a manual selection")

	// New items keep arriving; the manual selection still holds.
	_ = u.appendSessionMessage(streamedTextTurn("a2", "second"))
	_ = u.updateSessionMessage(streamedTextTurn("a2", "second, longer"))
	require.Equal(t, 10, u.chat.Selected())
	require.True(t, u.chat.HasManualSelection())

	// Selecting the newest item again releases the manual hold, and the
	// selection resumes following new items.
	u.chat.SelectLast()
	require.False(t, u.chat.HasManualSelection())
	_ = u.updateSessionMessage(streamedTextTurn("a2", "second, longer still"))
	require.Equal(t, u.chat.Len()-1, u.chat.Selected())
}

func TestChat_ManualSelectionPinLifecycle(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.chat.ScrollToBottom()
	u.chat.SelectLast()
	require.False(t, u.chat.HasManualSelection())

	// Moving the selection off the newest item pins it.
	u.chat.SelectPrev()
	require.True(t, u.chat.HasManualSelection())

	// Wheel-scrolling away keeps the pin, and scrolling back to the
	// bottom selects the newest item again, releasing it.
	u.applyChatScroll(-60)
	require.True(t, u.chat.HasManualSelection())
	u.applyChatScroll(60)
	require.True(t, u.chat.AtBottom())
	require.False(t, u.chat.HasManualSelection(), "reselecting the newest item must release the pin")

	// Loading a session resets the pin along with the messages.
	u.chat.SetSelected(0)
	require.True(t, u.chat.HasManualSelection())
	u.chat.SetMessages(frameTestItems("reload")...)
	require.False(t, u.chat.HasManualSelection())
}

func TestChat_MouseClickPinsManualSelection(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.chat.ScrollToBottom()
	u.chat.SelectLast()

	handled, _ := u.chat.HandleMouseDown(2, 3)
	require.True(t, handled)
	require.True(t, u.chat.HasManualSelection(), "clicking an older item must pin the selection")
	require.NotEqual(t, u.chat.Len()-1, u.chat.Selected())
}
