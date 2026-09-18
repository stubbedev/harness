package chat

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// QueuedMessageItem is a prompt queued behind the running turn, shown in
// the transcript the moment it is entered. It is a UI-local placeholder:
// nothing is persisted until the agent dequeues the prompt and creates
// the real user message, at which point the placeholder is dropped in
// the same update pass so the swap is invisible.
type QueuedMessageItem struct {
	*list.Versioned
	*cachedMessageItem
	*focusableMessageItem

	id   string
	text string
	sty  *styles.Styles
}

var _ MessageItem = (*QueuedMessageItem)(nil)

// NewQueuedMessageItem creates the transcript entry for a queued prompt.
func NewQueuedMessageItem(sty *styles.Styles, id, text string) *QueuedMessageItem {
	v := list.NewVersioned()
	return &QueuedMessageItem{
		Versioned:            v,
		cachedMessageItem:    &cachedMessageItem{},
		focusableMessageItem: newFocusableMessageItem(v),
		id:                   id,
		text:                 text,
		sty:                  sty,
	}
}

// Text returns the queued prompt's text.
func (q *QueuedMessageItem) Text() string { return q.text }

// UpdateText replaces the placeholder's prompt text: another prompt was
// queued and joins this entry. Cached renders are invalidated and the
// version bumped so the list re-renders the entry.
func (q *QueuedMessageItem) UpdateText(text string) {
	if q.text == text {
		return
	}
	q.text = text
	q.clearCache()
	q.Bump()
}

// ID implements [Identifiable].
func (q *QueuedMessageItem) ID() string { return q.id }

// Finished implements [list.Item]. A queued prompt is immutable until it
// materializes as a real message (a separate item).
func (q *QueuedMessageItem) Finished() bool { return true }

// RawRender implements [MessageItem]: the prompt's markdown, like a user
// message, prefixed with a dim clock glyph on the first line.
func (q *QueuedMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	content, _, ok := q.getCachedRender(cappedWidth)
	if ok {
		return content
	}

	renderer := common.UserMarkdownRenderer(q.sty, cappedWidth)
	mu := common.LockMarkdownRenderer(renderer)

	mu.Lock()
	result, err := renderer.Render(strings.TrimSpace(q.text))
	mu.Unlock()

	if err != nil {
		content = strings.TrimSpace(q.text)
	} else {
		content = strings.TrimSuffix(result, "\n")
	}

	lines := strings.Split(content, "\n")
	tag := q.sty.Resource.AdditionalText.Render(styles.QueuedIcon)
	if len(lines) > 0 && lines[0] != "" {
		// The markdown wrapped before the tag was prepended, so the
		// first line can now run past the width the renderer targeted;
		// trim it back with an ANSI-aware truncate.
		lines[0] = ansi.Truncate(tag+" "+lines[0], cappedWidth, "")
	} else {
		lines = []string{tag}
	}
	content = strings.Join(lines, "\n")

	height := lipgloss.Height(content)
	q.setCachedRender(content, cappedWidth, height)
	return content
}

// Render implements [MessageItem], with the same per-line focus prefix
// as user messages.
func (q *QueuedMessageItem) Render(width int) string {
	var key uint64
	if q.focused {
		key = 1
	}
	if cached, ok := q.getCachedPrefixedRender(width, key); ok {
		return cached
	}
	var prefix string
	if q.focused {
		prefix = q.sty.Messages.UserFocused.Render()
	} else {
		prefix = q.sty.Messages.UserBlurred.Render()
	}
	lines := strings.Split(q.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	q.setCachedPrefixedRender(out, width, key)
	return out
}

// HandleKeyEvent implements [KeyEventHandler]: a queued prompt copies
// as its own text, the same as the user message it becomes.
func (q *QueuedMessageItem) HandleKeyEvent(msg tea.KeyMsg, keys ItemKeymap) (bool, tea.Cmd) {
	if keys.MatchesCopy(msg) {
		return true, common.CopyToClipboard(q.text, copyToastMessage)
	}
	return false, nil
}
