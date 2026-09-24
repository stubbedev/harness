package chat

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/message"
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

	id    string
	texts []string
	sty   *styles.Styles
}

var _ MessageItem = (*QueuedMessageItem)(nil)

// NewQueuedMessageItem creates the transcript entry for one or more
// queued prompts.
func NewQueuedMessageItem(sty *styles.Styles, id string, texts []string) *QueuedMessageItem {
	v := list.NewVersioned()
	return &QueuedMessageItem{
		Versioned:            v,
		cachedMessageItem:    newCachedMessageItem(v),
		focusableMessageItem: newFocusableMessageItem(v),
		id:                   id,
		texts:                slices.Clone(texts),
		sty:                  sty,
	}
}

// Text returns the queued prompts' text.
func (q *QueuedMessageItem) Text() string {
	return strings.Join(q.texts, message.QueuedPromptSeparator)
}

// UpdateTexts replaces the placeholder's queued prompts: another
// prompt was queued and joins this entry. Cached renders are
// invalidated and the version bumped so the list re-renders the entry.
func (q *QueuedMessageItem) UpdateTexts(texts []string) {
	if slices.Equal(q.texts, texts) {
		return
	}
	q.texts = slices.Clone(texts)
	q.invalidate()
}

// ID implements [Identifiable].
func (q *QueuedMessageItem) ID() string { return q.id }

// Finished implements [list.Item]. A queued prompt is immutable until it
// materializes as a real message (a separate item).
func (q *QueuedMessageItem) Finished() bool { return true }

// RawRender implements [MessageItem]: the prompts' markdown, like user
// messages, prefixed with a dim clock glyph on the first line. A
// prompt queued as a named invocation renders as its compact row, the
// shape it takes once the real message lands.
func (q *QueuedMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	content, _, ok := q.getCachedRender(cappedWidth)
	if ok {
		return content
	}

	rendered := make([]string, 0, len(q.texts))
	for _, text := range q.texts {
		if name, _, isInvocation := message.ParsePromptInvocation(strings.TrimSpace(text)); isInvocation {
			rendered = append(rendered, promptInvocationRow(q.sty, name))
			continue
		}
		rendered = append(rendered, renderUserMarkdown(q.sty, strings.TrimSpace(text), cappedWidth))
	}
	content = strings.Join(rendered, message.QueuedPromptSeparator)

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
		return true, common.CopyToClipboard(q.Text(), copyToastMessage)
	}
	return false, nil
}
