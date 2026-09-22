package model

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/message"
)

// insertPastes is the shared harness for the inline-paste tests: paste
// the given payloads and return the UI, whose editor now references
// them by inline tokens.
func insertPastes(t *testing.T, pastes ...pastedAttachmentMsg) *UI {
	t.Helper()
	m := newBusyUI(&countingWorkspace{ready: true})
	m.textarea.Focus()
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

func textPaste(name, content string, lines int) pastedAttachmentMsg {
	return pastedAttachmentMsg{
		attachment: message.Attachment{
			FileName: name, FilePath: name, MimeType: "text/plain", Content: []byte(content),
		},
		lines: lines,
	}
}

// setEditorText replaces the editor text and parks the cursor at the
// given rune offset within the whole value.
func setEditorText(t *testing.T, m *UI, value string, col int) {
	t.Helper()
	m.textarea.SetValue(value)
	row := strings.Count(value[:col], "\n")
	lineStart := 0
	if row > 0 {
		lineStart = strings.LastIndex(value[:col], "\n") + 1
	}
	for m.textarea.Line() < row {
		m.textarea.CursorDown()
	}
	for m.textarea.Line() > row {
		m.textarea.CursorUp()
	}
	m.textarea.SetCursorColumn(col - lineStart)
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

// The deletion tests drive the real Update path: one helper per delete
// key, all of which must funnel through handleDeletionKey.
func backspaceKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyBackspace} }
func deleteKey() tea.KeyPressMsg    { return tea.KeyPressMsg{Code: tea.KeyDelete} }
func ctrlWKey() tea.KeyPressMsg     { return tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl} }
func ctrlBackspaceKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModCtrl}
}
func ctrlUKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl} }
func ctrlKKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl} }

// typeKeys types one keystroke into the editor.
func typeKeys(t *testing.T, m *UI, msg tea.KeyPressMsg) {
	t.Helper()
	m.Update(msg)
}

// TestDeletionBackspaceUnit pins the backspace contract: the whole
// token and the space its insertion added go at once, the cursor lands
// on the token's start, and the payload leaves the store with the
// token. The cursor may sit right behind or anywhere inside the token.
func TestDeletionBackspaceUnit(t *testing.T) {
	t.Parallel()

	m := insertPastes(t, imagePaste("paste_1.png"))
	setEditorText(t, m, "x [Image #1] y", 12) // right after the token

	typeKeys(t, m, backspaceKey())
	require.Equal(t, "x y", m.textarea.Value())
	require.Equal(t, 2, m.textarea.Column(), "cursor moves to the token's start")
	require.Empty(t, m.pastedAttachments, "the payload goes with the token")

	m = insertPastes(t, imagePaste("paste_1.png"))
	setEditorText(t, m, "pre [Image #1] post", 7) // inside the token
	typeKeys(t, m, backspaceKey())
	require.Equal(t, "pre post", m.textarea.Value())
	require.Equal(t, 4, m.textarea.Column())
	require.Empty(t, m.pastedAttachments)
}

// TestDeletionBackspaceMultiLine pins the cursor restoration when the
// token sits above other lines: the surviving text and the cursor
// position are untouched apart from the token.
func TestDeletionBackspaceMultiLine(t *testing.T) {
	t.Parallel()

	m := insertPastes(t, imagePaste("paste_1.png"))
	setEditorText(t, m, "see [Image #1]\nnext line", 13)

	typeKeys(t, m, backspaceKey())
	require.Equal(t, "see \nnext line", m.textarea.Value())
	require.Equal(t, 0, m.textarea.Line())
	require.Equal(t, 4, m.textarea.Column())
	require.Empty(t, m.pastedAttachments)
}

// TestDeletionForwardUnit pins the forward delete at the token: the
// unit goes whole, trailing space included. (ctrl+d is not a deletion
// here: the global details toggle owns it.)
func TestDeletionForwardUnit(t *testing.T) {
	t.Parallel()

	m := insertPastes(t, imagePaste("paste_1.png"))
	setEditorText(t, m, "x [Image #1] y", 2) // right before the token

	typeKeys(t, m, deleteKey())
	require.Equal(t, "x y", m.textarea.Value())
	require.Equal(t, 2, m.textarea.Column())
	require.Empty(t, m.pastedAttachments)

	m = insertPastes(t, imagePaste("paste_1.png"))
	setEditorText(t, m, "pre [Image #1] post", 7) // inside the token
	typeKeys(t, m, deleteKey())
	require.Equal(t, "pre post", m.textarea.Value())
	require.Empty(t, m.pastedAttachments)
}

// TestDeletionWordUnit pins the word deletions (ctrl+w, ctrl+backspace,
// alt+delete): the token is the unit, including the space behind it
// when the cursor sits right after that space.
func TestDeletionWordUnit(t *testing.T) {
	t.Parallel()

	for _, key := range []tea.KeyPressMsg{ctrlWKey(), ctrlBackspaceKey()} {
		m := insertPastes(t, imagePaste("paste_1.png"))
		setEditorText(t, m, "x [Image #1] y", 13) // right after the trailing space

		typeKeys(t, m, key)
		require.Equal(t, "x y", m.textarea.Value())
		require.Equal(t, 2, m.textarea.Column())
		require.Empty(t, m.pastedAttachments)

		m = insertPastes(t, imagePaste("paste_1.png"))
		setEditorText(t, m, "pre [Image #1] post", 7)
		typeKeys(t, m, key)
		require.Equal(t, "pre post", m.textarea.Value())
		require.Equal(t, 4, m.textarea.Column())
	}

	m := insertPastes(t, imagePaste("paste_1.png"))
	setEditorText(t, m, "pre [Image #1] post", 4) // right before the token
	typeKeys(t, m, tea.KeyPressMsg{Code: tea.KeyDelete, Mod: tea.ModAlt})
	require.Equal(t, "pre post", m.textarea.Value())
	require.Equal(t, 4, m.textarea.Column())
	require.Empty(t, m.pastedAttachments)
}

// TestDeletionKillToEdgeUnit pins the line kills: ctrl+k from inside a
// token takes the whole token with the killed tail, ctrl+u takes it
// with the killed head.
func TestDeletionKillToEdgeUnit(t *testing.T) {
	t.Parallel()

	m := insertPastes(t, imagePaste("paste_1.png"))
	setEditorText(t, m, "pre [Image #1] post", 7)
	typeKeys(t, m, ctrlKKey())
	require.Equal(t, "pre ", m.textarea.Value())
	require.Empty(t, m.pastedAttachments)

	m = insertPastes(t, imagePaste("paste_1.png"))
	setEditorText(t, m, "pre [Image #1] post", 10)
	typeKeys(t, m, ctrlUKey())
	require.Equal(t, "post", m.textarea.Value())
	require.Empty(t, m.pastedAttachments)
}

// TestDeletionMisses pins the guard: deletions that do not touch a
// token fall through to the textarea's own handling, and a half-deleted
// token is no longer a reference.
func TestDeletionMisses(t *testing.T) {
	t.Parallel()

	m := insertPastes(t, imagePaste("paste_1.png"))

	// Cursor right before the token: an ordinary backspace.
	setEditorText(t, m, "x [Image #1] y", 2)
	typeKeys(t, m, backspaceKey())
	require.Equal(t, "x[Image #1] y", m.textarea.Value())
	require.Len(t, m.pastedAttachments, 1)

	// Cursor right after the trailing space: only the space goes.
	setEditorText(t, m, "x [Image #1] y", 13)
	typeKeys(t, m, backspaceKey())
	require.Equal(t, "x [Image #1]y", m.textarea.Value())
	require.Len(t, m.pastedAttachments, 1)

	// A half-deleted token is no longer a reference.
	setEditorText(t, m, "x [Image #1", 11)
	typeKeys(t, m, backspaceKey())
	require.Equal(t, "x [Image #", m.textarea.Value())
	require.Len(t, m.pastedAttachments, 1)
}

// TestDeletionSelectionUnit pins the selection deletions: a backspace
// over a selection that touches a token deletes the token whole, and
// the cut keeps copying what the user selected.
func TestDeletionSelectionUnit(t *testing.T) {
	t.Parallel()

	m := insertPastes(t, imagePaste("paste_1.png"))
	setEditorText(t, m, "x [Image #1] y", 8) // inside the token

	// Select "e #1" with shift+right, then backspace over it.
	for range 4 {
		typeKeys(t, m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	}
	typeKeys(t, m, backspaceKey())
	require.Equal(t, "x y", m.textarea.Value())
	require.Empty(t, m.pastedAttachments, "a token the selection touches goes whole")

	// Cut over a token: the clipboard keeps the selected text, the
	// token goes whole.
	m = insertPastes(t, imagePaste("paste_1.png"))
	setEditorText(t, m, "x [Image #1] y", 8)
	for range 2 {
		typeKeys(t, m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	}
	typeKeys(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl | tea.ModShift})
	require.Equal(t, "x y", m.textarea.Value())
	require.Empty(t, m.pastedAttachments)
}

// TestPasteTokenKinds pins the classification the shared RefKind drives:
// text pastes keep their line count in the token, files and images get
// their own kinds, so a pasted PDF no longer labels itself "Pasted text".
func TestPasteTokenKinds(t *testing.T) {
	t.Parallel()

	m := insertPastes(t,
		textPaste("paste_1.txt", "a\nb\nc", 3),
		pastedAttachmentMsg{attachment: message.Attachment{
			FileName: "paste_2.pdf", FilePath: "paste_2.pdf",
			MimeType: "application/pdf", Content: []byte("%PDF"),
		}},
	)
	require.Equal(t, "[Pasted text #1 +3 lines] [File #1] ", m.textarea.Value())
}
