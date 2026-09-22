package model

import (
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/stubbedev/harness/internal/message"
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
	kind, lines := msg.attachment.RefKind(), 0
	if kind == message.RefKindPaste {
		lines = msg.lines
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
// stay short and stable across the editor's life. The format is the
// shared FormatRef, the one the transcript tags are minted with.
func (m *UI) mintPasteToken(kind string, lines int) string {
	for n := 1; ; n++ {
		token := message.FormatRef(kind, n, lines)
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

// handleDeletionKey is the single interception point of every text
// deletion in the editor: backspace, ctrl+h, delete, ctrl+d, the word
// deletions, ctrl+k and ctrl+u all offer their naive deletion range
// here first. Any paste token the range touches is deleted whole,
// payload included, by deleteAsUnits below instead of the textarea; a
// deletion that touches no token falls through untouched.
func (m *UI) handleDeletionKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	curValue := m.textarea.Value()
	from, to, ok := m.deletionRange(msg)
	if !ok {
		return false, nil
	}
	prevHeight := m.textarea.Height()
	if !m.deleteAsUnits(from, to) {
		return false, nil
	}
	// Mirror the housekeeping the textarea path would have done.
	if m.bangMode {
		if val := m.textarea.Value(); val == "" && curValue != "" {
			m.bangWasEmpty = true
		} else {
			m.bangWasEmpty = false
		}
	}
	m.updateHistoryDraft(curValue)
	return true, m.handleTextareaHeightChange(prevHeight)
}

// deletionRange maps a deletion keystroke to the absolute rune-offset
// range of the editor value it would delete: the single place that
// knows every deletion key's semantics. Deletions over an active
// selection delete the selection; the rest derive from the cursor. ok
// is false for keystrokes that are not deletions or that have nothing
// to delete.
func (m *UI) deletionRange(msg tea.KeyPressMsg) (from, to int, ok bool) {
	ta := m.textarea
	value := m.textarea.Value()
	row, col := ta.Line(), ta.Column()
	off := absoluteOffset(value, row, col)
	lineStart := off - col
	lineLen := len([]rune(strings.Split(value, "\n")[row]))

	isDeletion := key.Matches(msg, ta.KeyMap.DeleteCharacterBackward) ||
		key.Matches(msg, ta.KeyMap.DeleteCharacterForward) ||
		key.Matches(msg, m.keyMap.Editor.DeleteWordBackward) ||
		key.Matches(msg, ta.KeyMap.DeleteWordForward) ||
		key.Matches(msg, ta.KeyMap.DeleteAfterCursor) ||
		key.Matches(msg, ta.KeyMap.DeleteBeforeCursor)
	if !isDeletion {
		return 0, 0, false
	}
	// Any deletion over an active selection deletes the selection.
	if ta.HasSelection() {
		return m.selectionRange()
	}

	switch {
	case key.Matches(msg, ta.KeyMap.DeleteCharacterBackward):
		if off == 0 {
			return 0, 0, false
		}
		return off - 1, off, true
	case key.Matches(msg, ta.KeyMap.DeleteCharacterForward):
		if off >= len([]rune(value)) {
			return 0, 0, false
		}
		return off, off + 1, true
	case key.Matches(msg, m.keyMap.Editor.DeleteWordBackward):
		return tokenBehindCursor([]rune(value), off, row)
	case key.Matches(msg, ta.KeyMap.DeleteWordForward):
		return tokenAheadCursor([]rune(value), off, row)
	case key.Matches(msg, ta.KeyMap.DeleteAfterCursor):
		if col >= lineLen {
			return 0, 0, false
		}
		return off, lineStart + lineLen, true
	case key.Matches(msg, ta.KeyMap.DeleteBeforeCursor):
		if col == 0 {
			return 0, 0, false
		}
		return lineStart, off, true
	default:
		return 0, 0, false
	}
}

// tokenBehindCursor and tokenAheadCursor give the word deletions their
// reach: a word deletion takes a paste token when the token is the
// unit at or behind the cursor, with the trailing space its insertion
// added when the cursor sits behind that space. tokenBehindCursor
// keeps [tokenStart, cursor) as the naive range; tokenAheadCursor
// starts at the cursor, which must sit right before the token.
func tokenBehindCursor(value []rune, off, row int) (from, to int, ok bool) {
	start, _, ok := tokenAtCursor(value, off, row, reachInsideOrBehind)
	if !ok {
		return 0, 0, false
	}
	return start, off, true
}

func tokenAheadCursor(value []rune, off, row int) (from, to int, ok bool) {
	start, end, ok := tokenAtCursor(value, off, row, reachAtStart)
	if !ok {
		return 0, 0, false
	}
	return start, end, true
}

type tokenReach uint8

const (
	reachInsideOrBehind tokenReach = iota
	reachAtStart
)

// tokenAtCursor finds the paste token whose unit covers the cursor on
// the cursor's line: the token plus the trailing space its insertion
// added, the same way the send-time resolution strips "token " as one
// piece. reachInsideOrBehind wants the cursor strictly inside the unit;
// reachAtStart wants it exactly on the token's opening bracket.
func tokenAtCursor(value []rune, off, row int, reach tokenReach) (start, end int, ok bool) {
	lineStart := absoluteOffset(string(value), row, 0)
	col := off - lineStart
	line := lineOf(value, row)
	for i, r := range line {
		if r != '[' {
			continue
		}
		close := strings.IndexRune(string(line[i:]), ']')
		if close < 0 {
			continue
		}
		unit := lineStart + i + close + 1
		if unit < len(value) && value[unit] == ' ' {
			unit++
		}
		inside := col > i && col <= unit-lineStart
		atStart := col == i
		taken := inside || (reach == reachAtStart && atStart)
		if taken {
			return lineStart + i, unit, true
		}
	}
	return 0, 0, false
}

// deleteAsUnits is the one deletion applier: the range [from, to) is
// expanded to whole paste tokens it touches and deleted with the
// cursor parked at the expanded range's start. It reports whether a
// token was involved; false hands the deletion back to the textarea.
func (m *UI) deleteAsUnits(from, to int) bool {
	runes := []rune(m.textarea.Value())
	if from < 0 || to > len(runes) || from >= to {
		return false
	}
	lo, hi := from, to
	var tokens []string
	for i := 0; i < len(runes); i++ {
		if runes[i] != '[' {
			continue
		}
		close := strings.IndexRune(string(runes[i:]), ']')
		if close < 0 {
			continue
		}
		end := i + close + 1
		token := string(runes[i:end])
		if _, _, _, ok := message.ParseRef(token); !ok || i >= hi || end <= lo {
			continue
		}
		lo, hi = min(lo, i), max(hi, end)
		// The space the insertion added is part of the unit when the
		// deletion reaches the token's right edge.
		if hi == end && end < len(runes) && runes[end] == ' ' {
			hi++
		}
		tokens = append(tokens, token)
		i = end - 1
	}
	if len(tokens) == 0 {
		return false
	}

	newRunes := append(append([]rune{}, runes[:lo]...), runes[hi:]...)
	m.textarea.SetValue(string(newRunes))
	row, col := rowColOfOffset(newRunes, lo)
	for m.textarea.Line() > row {
		m.textarea.CursorUp()
	}
	m.textarea.SetCursorColumn(col)

	for _, token := range tokens {
		delete(m.pastedAttachments, token)
	}
	return true
}

// selectionRange gives the active selection as an absolute-offset
// range, oriented low-to-high.
func (m *UI) selectionRange() (from, to int, ok bool) {
	selStart, selEnd, ok := m.textarea.Selection()
	if !ok {
		return 0, 0, false
	}
	value := m.textarea.Value()
	return absoluteOffset(value, selStart.Row, selStart.Col),
		absoluteOffset(value, selEnd.Row, selEnd.Col), true
}

// deleteSelectionAsUnits deletes the active selection through the
// token-unit rule; false hands it to the textarea's own deletion.
func (m *UI) deleteSelectionAsUnits() bool {
	from, to, ok := m.selectionRange()
	if !ok {
		return false
	}
	return m.deleteAsUnits(from, to)
}

// absoluteOffset maps a (row, col) cursor position into the value's
// absolute rune offsets; col is a rune index within the line.
func absoluteOffset(value string, row, col int) int {
	offset := 0
	lines := strings.Split(value, "\n")
	for i := 0; i < row && i < len(lines); i++ {
		offset += len([]rune(lines[i])) + 1
	}
	return offset + col
}

// rowColOfOffset maps an absolute rune offset back to (row, col).
func rowColOfOffset(runes []rune, offset int) (row, col int) {
	lineStart := 0
	for i, r := range runes {
		if r == '\n' {
			if i >= offset {
				break
			}
			row++
			lineStart = i + 1
		}
	}
	return row, offset - lineStart
}

// lineOf returns the row'th line of the value as runes.
func lineOf(value []rune, row int) []rune {
	return []rune(strings.Split(string(value), "\n")[row])
}
