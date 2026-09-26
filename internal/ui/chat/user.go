package chat

import (
	"encoding/xml"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// skillInvocation represents the XML structure for a loaded skill.
type skillInvocation struct {
	Name         string `xml:"name"`
	Description  string `xml:"description"`
	Location     string `xml:"location"`
	Instructions string `xml:"instructions"`
}

// attachmentHeadTailLines is how many lines a truncated text attachment
// shows on each side of the rule.
const attachmentHeadTailLines = 3

// UserMessageItem represents a user message in the chat UI.
type UserMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	message *message.Message
	sty     *styles.Styles
	// promptExpanded shows a prompt invocation's full body under its
	// compact row. Only meaningful when promptInvocation reports ok.
	promptExpanded bool
}

var (
	_ MessageItem = (*UserMessageItem)(nil)
	_ Expandable  = (*UserMessageItem)(nil)
)

// NewUserMessageItem creates a new UserMessageItem.
func NewUserMessageItem(sty *styles.Styles, message *message.Message) MessageItem {
	v := list.NewVersioned()
	return &UserMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        newCachedMessageItem(v),
		focusableMessageItem:     newFocusableMessageItem(v),
		message:                  message,
		sty:                      sty,
	}
}

// Finished implements list.Item. User messages are immutable once
// submitted, so the entry is always safe to freeze.
func (m *UserMessageItem) Finished() bool {
	return true
}

// RawRender implements [MessageItem].
func (m *UserMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	content, height, ok := m.getCachedRender(cappedWidth)
	// cache hit
	if ok {
		return m.renderHighlighted(content, cappedWidth, height)
	}

	msgContent := strings.TrimSpace(m.message.Content().Text)
	inv := m.promptInvocation()

	switch {
	case strings.HasPrefix(msgContent, "<loaded_skill>"):
		// A skill invocation carries its own compact rendering.
		content = m.renderSkillInvocation(msgContent, cappedWidth)
	case inv != nil:
		content = m.renderPromptInvocation(inv, cappedWidth)
	default:
		content = renderUserMarkdown(m.sty, msgContent, cappedWidth)

		if len(m.message.BinaryContent()) > 0 {
			attachmentsStr := m.renderAttachments(cappedWidth)
			if content == "" {
				content = attachmentsStr
			} else {
				content = strings.Join([]string{content, "", attachmentsStr}, "\n")
			}
		}
	}

	height = lipgloss.Height(content)
	m.setCachedRender(content, cappedWidth, height)
	return m.renderHighlighted(content, cappedWidth, height)
}

// promptInvocation returns the wrapped prompt invocation the message
// carries, or nil when the message is ordinary typed input.
func (m *UserMessageItem) promptInvocation() *promptInvocationBody {
	name, body, ok := message.ParsePromptInvocation(strings.TrimSpace(m.message.Content().Text))
	if !ok {
		return nil
	}
	return &promptInvocationBody{name: name, body: body}
}

// promptInvocationBody is the parsed form of a wrapped prompt
// invocation. A pointer to it doubles as the "is one" flag.
type promptInvocationBody struct {
	name string
	body string
}

// renderPromptInvocation renders the message's prompt invocation: the
// compact row, plus the full prompt body once expanded.
func (m *UserMessageItem) renderPromptInvocation(inv *promptInvocationBody, width int) string {
	if !m.promptExpanded {
		return promptInvocationRow(m.sty, inv.name)
	}
	return promptInvocationRow(m.sty, inv.name) + "\n\n" + renderUserMarkdown(m.sty, inv.body, width)
}

// ToggleExpanded implements [Expandable]. A prompt invocation collapses
// to its compact row and expands to the full prompt body; any other user
// message is a no-op reported collapsed, so the expand key never claims
// text the user typed.
func (m *UserMessageItem) ToggleExpanded() bool {
	m.SetExpansionLevel(expansionLevel(!m.promptExpanded))
	return m.promptExpanded
}

// ExpansionLevel implements [Expandable]: 1 while a prompt invocation
// shows its full body.
func (m *UserMessageItem) ExpansionLevel() uint8 {
	return expansionLevel(m.promptExpanded)
}

// SetExpansionLevel implements [Expandable]. Only a prompt invocation
// has a body to expand; any other user message stays collapsed.
func (m *UserMessageItem) SetExpansionLevel(level uint8) {
	if m.promptInvocation() != nil && applyExpansionLevel(&m.promptExpanded, level) {
		m.invalidate()
	}
}

// renderSkillInvocation renders a loaded_skill XML as a special UI element.
func (m *UserMessageItem) renderSkillInvocation(content string, width int) string {
	var skill skillInvocation
	if err := xml.Unmarshal([]byte(content), &skill); err != nil {
		// If parsing fails, just render as markdown.
		return renderUserMarkdown(m.sty, content, width)
	}

	return toolOutputSkillContent(m.sty, skill.Name, skill.Description)
}

// Render implements MessageItem.
func (m *UserMessageItem) Render(width int) string {
	// Bypass the prefix cache while a highlight range is active so
	// selection drags reflect immediately without invalidating the
	// cache. Highlight changes are intentionally applied "above" the
	// prefix cache.
	useCache := !m.isHighlighted()
	var key uint64
	if m.focused {
		key = 1
	}
	if useCache {
		if cached, ok := m.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	var prefix string
	if m.focused {
		prefix = m.sty.Messages.UserFocused.Render()
	} else {
		prefix = m.sty.Messages.UserBlurred.Render()
	}
	lines := strings.Split(m.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	if useCache {
		m.setCachedPrefixedRender(out, width, key)
	}
	return out
}

// ID implements MessageItem.
func (m *UserMessageItem) ID() string {
	return m.message.ID
}

// renderAttachments renders the message's attachments as they read:
// text attachments as their own text, truncated head-and-tail around a
// rule when the paste is big, and everything else as numbered tags
// ([Image #1], [File #2]) minted by the same FormatRef the editor's
// inline tokens use. The tags count across the message, so the first
// two non-text attachments render [Image #1] [File #2].
func (m *UserMessageItem) renderAttachments(width int) string {
	var blocks, tags []string
	for _, bc := range m.message.BinaryContent() {
		att := message.Attachment{MimeType: bc.MIMEType, Content: bc.Data}
		if att.IsText() {
			blocks = append(blocks, renderTextAttachment(string(bc.Data), width, m.sty))
			continue
		}
		tags = append(tags, m.sty.Messages.AttachmentTag.Render(
			message.FormatRef(att.RefKind(), len(tags)+1, 0)))
	}
	if len(tags) > 0 {
		blocks = append(blocks, strings.Join(tags, " "))
	}
	return strings.Join(blocks, "\n")
}

// renderTextAttachment renders a text attachment as its own text. A
// paste too big to have lived in the prompt shows its head and tail
// lines around a rule line, the shape the editor's token stood in for;
// every shown line is clamped to the width.
func renderTextAttachment(text string, width int, sty *styles.Styles) string {
	text = strings.TrimRight(text, "\n")
	lines := strings.Split(text, "\n")
	if len(lines) <= message.PasteLinesThreshold {
		return text
	}
	head := lines[:attachmentHeadTailLines]
	tail := lines[len(lines)-attachmentHeadTailLines:]
	clamp := func(s string) string { return ansi.Truncate(s, width, "…") }
	return strings.Join([]string{
		strings.Join(mapLines(head, clamp), "\n"),
		sty.Messages.PasteRule.Render(clamp(strings.Repeat("─", width))),
		strings.Join(mapLines(tail, clamp), "\n"),
	}, "\n")
}

// mapLines applies fn to every line.
func mapLines(lines []string, fn func(string) string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = fn(line)
	}
	return out
}

// HandleKeyEvent implements KeyEventHandler.
func (m *UserMessageItem) HandleKeyEvent(msg tea.KeyMsg, keys ItemKeymap) (bool, tea.Cmd) {
	if keys.MatchesCopy(msg) {
		text := copyJoin(copyMessageText(m.message), copyAttachments(m.message))
		return true, common.CopyToClipboard(text, copyToastMessage)
	}
	return false, nil
}
