package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

// TestValidateCallRequiresSomethingToSend pins the structural gate: a call
// needs a prompt or an attachment to send on its own, and it always needs a
// session. An image is a prompt in its own right, so an image-only call is
// valid.
func TestValidateCallRequiresSomethingToSend(t *testing.T) {
	t.Parallel()

	image := message.Attachment{FileName: "cat.png", MimeType: "image/png", Content: []byte("png")}
	text := message.Attachment{FileName: "notes.txt", MimeType: "text/plain", Content: []byte("hi")}

	require.NoError(t, ValidateCall(SessionAgentCall{
		SessionID:   "s1",
		Attachments: []message.Attachment{image},
	}), "an image-only prompt is a prompt")
	require.NoError(t, ValidateCall(SessionAgentCall{
		SessionID:   "s1",
		Attachments: []message.Attachment{text},
	}))
	require.NoError(t, ValidateCall(SessionAgentCall{SessionID: "s1", Prompt: "hi"}))

	require.ErrorIs(t, ValidateCall(SessionAgentCall{SessionID: "s1"}), ErrEmptyPrompt)
	require.ErrorIs(t, ValidateCall(SessionAgentCall{Prompt: "hi"}), ErrSessionMissing)
}
