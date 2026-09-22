package model

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/message"
)

// insertPastes is the shared harness for the inline-paste tests: paste
// the given payloads and return the UI, whose editor now references
// them by inline tokens.
func insertPastes(t *testing.T, pastes ...pastedAttachmentMsg) *UI {
	t.Helper()
	m := newBusyUI(&countingWorkspace{ready: true})
	for _, p := range pastes {
		m.insertPastedAttachment(p)
	}
	return m
}

func imagePaste(name string) pastedAttachmentMsg {
	return pastedAttachmentMsg{
		attachment: message.Attachment{
			FileName: name, FilePath: name, MimeType: "image/png", Content: []byte(name),
		},
	}
}

// TestPastedAttachmentsInsertInlineTokens pins the paste flow: a paste
// drops a placeholder token into the editor at the cursor instead of a
// pill in the attachments strip, images and text paste under their own
// kinds, and numbering stays dense.
func TestPastedAttachmentsInsertInlineTokens(t *testing.T) {
	t.Parallel()

	m := insertPastes(t,
		imagePaste("paste_1.png"),
		imagePaste("paste_2.png"),
		pastedAttachmentMsg{
			attachment: message.Attachment{
				FileName: "paste_1.txt", FilePath: "paste_1.txt",
				MimeType: "text/plain", Content: []byte("a\nb\nc"),
			},
			lines: 3,
		},
	)

	require.Equal(t, "[Image #1] [Image #2] [Pasted text #1 +3 lines] ", m.textarea.Value())
	require.Len(t, m.attachments.List(), 0, "pastes must not join the attachments strip")
	require.Len(t, m.pastedAttachments, 3)
}

// TestResolvePastedAttachments pins the send-time resolution: tokens
// are swapped for their payloads and stripped from the prompt, a token
// the user deleted takes its attachment with it, and the store is
// consumed.
func TestResolvePastedAttachments(t *testing.T) {
	t.Parallel()

	m := insertPastes(t, imagePaste("paste_1.png"), imagePaste("paste_2.png"))
	m.textarea.SetValue("compare [Image #1] with the other one")
	// The user deleted the second token; its paste goes with it.
	text, attachments := m.resolvePastedAttachments(m.textarea.Value())
	require.Equal(t, "compare with the other one", text)
	require.Len(t, attachments, 1)
	require.Equal(t, "paste_1.png", attachments[0].FileName)
	require.Empty(t, m.pastedAttachments, "sending consumes the token store")

	// A clean editor resolves to itself with no attachments.
	text, attachments = m.resolvePastedAttachments("plain prompt")
	require.Equal(t, "plain prompt", text)
	require.Empty(t, attachments)
}

// TestResolvePastedAttachmentsKeepsOrder pins the attachment order to
// the prompt's own token order.
func TestResolvePastedAttachmentsKeepsOrder(t *testing.T) {
	t.Parallel()

	m := insertPastes(t, imagePaste("paste_1.png"), imagePaste("paste_2.png"), imagePaste("paste_3.png"))
	m.textarea.Reset()
	m.textarea.InsertString("third [Image #3] then first [Image #1]")

	_, attachments := m.resolvePastedAttachments(m.textarea.Value())
	require.Len(t, attachments, 2)
	require.Equal(t, "paste_3.png", attachments[0].FileName)
	require.Equal(t, "paste_1.png", attachments[1].FileName)
}
