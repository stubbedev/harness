package model

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stubbedev/harness/internal/message"
)

const (
	// pasteImageKind labels image pastes in their inline token.
	pasteImageKind = "Image"
	// pasteTextKind labels text pastes in their inline token.
	pasteTextKind = "Pasted text"
)

// pastedAttachmentMsg carries a paste that became an attachment. Rather
// than joining the attachments strip as a pill, it is referenced inline
// in the editor by a placeholder token ("[Image #1]",
// "[Pasted text #1 +24 lines]"), so the prompt reads the way it will be
// answered and deleting the token deletes the paste.
type pastedAttachmentMsg struct {
	attachment message.Attachment
	// lines is the pasted content's line count, shown in a text
	// paste's token; zero omits the suffix.
	lines int
}

// insertPastedAttachment mints the paste's inline token, stores the
// payload under it, and drops the token into the editor at the cursor.
func (m *UI) insertPastedAttachment(msg pastedAttachmentMsg) {
	kind, lines := pasteTextKind, msg.lines
	if strings.HasPrefix(msg.attachment.MimeType, "image/") {
		kind, lines = pasteImageKind, 0
	}
	token := m.mintPasteToken(kind, lines)
	if m.pastedAttachments == nil {
		m.pastedAttachments = make(map[string]message.Attachment)
	}
	m.pastedAttachments[token] = msg.attachment

	prevHeight := m.textarea.Height()
	m.textarea.InsertString(token + " ")
	_ = m.handleTextareaHeightChange(prevHeight)
}

// mintPasteToken returns the next free inline token for a paste kind:
// the smallest number nothing else in the editor claims, so numbers
// stay short and stable across the editor's life.
func (m *UI) mintPasteToken(kind string, lines int) string {
	for n := 1; ; n++ {
		token := fmt.Sprintf("[%s #%d", kind, n)
		if lines > 0 {
			token += fmt.Sprintf(" +%d lines", lines)
		}
		token += "]"
		if _, taken := m.pastedAttachments[token]; !taken {
			return token
		}
	}
}

// resolvePastedAttachments splits the editor text into its clean prompt
// and the pastes its tokens still reference, consuming the token store:
// tokens the user deleted take their attachments with them, and a sent
// prompt leaves nothing behind. Tokens are replaced together with the
// space that followed their insertion, so the surviving text keeps its
// shape.
func (m *UI) resolvePastedAttachments(text string) (string, []message.Attachment) {
	if len(m.pastedAttachments) == 0 {
		return text, nil
	}

	present := make([]string, 0, len(m.pastedAttachments))
	for token := range m.pastedAttachments {
		if strings.Contains(text, token) {
			present = append(present, token)
		}
	}
	// Order by where the tokens sit in the text, so the attachments
	// array follows the prompt's own order and the output is stable.
	sort.Slice(present, func(i, j int) bool {
		return strings.Index(text, present[i]) < strings.Index(text, present[j])
	})

	attachments := make([]message.Attachment, 0, len(present))
	for _, token := range present {
		attachments = append(attachments, m.pastedAttachments[token])
		// Take the space the insertion added with the token, so the
		// surviving text keeps single spacing; a bare token (cursor
		// edits) falls through to the plain removal.
		text = strings.ReplaceAll(text, token+" ", "")
		text = strings.ReplaceAll(text, token, "")
	}
	clear(m.pastedAttachments)
	return strings.TrimSpace(text), attachments
}
