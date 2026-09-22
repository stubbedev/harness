package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// newTestUserItem builds a UserMessageItem carrying text.
func newTestUserItem(t *testing.T, text string) *UserMessageItem {
	t.Helper()
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:    "user-1",
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: text}},
	}
	item := NewUserMessageItem(&sty, msg)
	userItem, ok := item.(*UserMessageItem)
	require.True(t, ok, "NewUserMessageItem must return *UserMessageItem")
	return userItem
}

// newTestAttachmentUserItem builds a UserMessageItem carrying text plus
// the given binary content parts.
func newTestAttachmentUserItem(t *testing.T, text string, parts ...message.ContentPart) *UserMessageItem {
	t.Helper()
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:    "user-1",
		Role:  message.User,
		Parts: append([]message.ContentPart{message.TextContent{Text: text}}, parts...),
	}
	item := NewUserMessageItem(&sty, msg)
	userItem, ok := item.(*UserMessageItem)
	require.True(t, ok)
	return userItem
}

// renderedLines returns the rendered message as trimmed, non-empty-aware
// lines so assertions can talk about visual line structure without
// depending on ANSI styling or trailing pad.
func renderedLines(t *testing.T, text string, width int) []string {
	t.Helper()
	out := newTestUserItem(t, text).RawRender(width)
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(ansi.Strip(l))
	}
	return lines
}

// TestUserMessagePreservesSingleLineBreaks is the regression test for
// stubbedev/harness#3502: a user submitting
//
//	a
//	b
//
//	c
//
// saw "a" and "b" collapsed onto one line ("ab"/"a b") in the chat
// display, because user input was rendered through the standard
// Markdown renderer where a lone newline is a soft break. The history
// sent to the model was always correct; only the display was wrong.
func TestUserMessagePreservesSingleLineBreaks(t *testing.T) {
	t.Parallel()

	lines := renderedLines(t, "a\nb\n\nc", 80)

	var got []string
	for _, l := range lines {
		if l != "" {
			got = append(got, l)
		}
	}
	require.Equal(t, []string{"a", "b", "c"}, got,
		"each typed line must render on its own visual line")

	// The blank line the user typed between "b" and "c" must survive as a
	// paragraph break, so this is not merely "everything got hard-wrapped".
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "b\n\nc",
		"the blank line between paragraphs must be preserved")
}

// TestUserMessageSoftWrapsLongLines guards the other direction: a
// single long line that exceeds the render width must still soft wrap.
// Preserving *authored* newlines must not disable wrapping.
func TestUserMessageSoftWrapsLongLines(t *testing.T) {
	t.Parallel()

	long := "this is a single very long line of user input that comfortably exceeds the render width and therefore has to be soft wrapped by the renderer"
	lines := renderedLines(t, long, 40)

	var nonEmpty int
	for _, l := range lines {
		if l != "" {
			nonEmpty++
		}
		require.LessOrEqual(t, len(l), 40,
			"no rendered line may exceed the render width")
	}
	require.Greater(t, nonEmpty, 1,
		"a line longer than the width must wrap onto multiple lines")
}

// TestUserMessageMarkdownConstructsUnaffected pins the blast radius of
// the #3502 fix: preserving newlines must not disturb block-level
// Markdown that users legitimately paste into the prompt.
func TestUserMessageMarkdownConstructsUnaffected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "bullet list keeps one item per line",
			input: "- one\n- two\n- three",
			want:  []string{"one", "two", "three"},
		},
		{
			name:  "numbered list keeps one item per line",
			input: "1. first\n2. second",
			want:  []string{"first", "second"},
		},
		{
			name:  "fenced code block keeps its lines",
			input: "```go\nfunc main() {\n\tx := 1\n}\n```",
			want:  []string{"func main() {", "x := 1", "}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			joined := strings.Join(renderedLines(t, tt.input, 80), "\n")
			for _, w := range tt.want {
				require.Contains(t, joined, w)
			}
		})
	}
}

// attachmentLines returns the rendered attachment block of a message
// carrying text plus binary content, as stripped lines.
func attachmentLines(t *testing.T, text string, parts ...message.ContentPart) []string {
	t.Helper()
	out := newTestAttachmentUserItem(t, text, parts...).RawRender(80)
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(ansi.Strip(l))
	}
	return lines
}

// TestUserMessageTextAttachmentShownAsText pins the transcript shape:
// a text attachment's content is shown as its own text, not as a chip.
func TestUserMessageTextAttachmentShownAsText(t *testing.T) {
	t.Parallel()

	lines := attachmentLines(t, "look at this",
		message.BinaryContent{Path: "paste_1.txt", MIMEType: "text/plain", Data: []byte("a\nb\nc")})
	require.Equal(t, []string{"look at this", "a", "b", "c"}, nonEmptyLines(lines))
}

// TestUserMessageBigTextAttachmentTruncatedAroundRule pins the big-paste
// shape: head and tail lines around a rule, the shape the editor's
// token stood in for, with every shown line clamped to the width.
func TestUserMessageBigTextAttachmentTruncatedAroundRule(t *testing.T) {
	t.Parallel()

	content := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nl11\nl12"
	lines := attachmentLines(t, "see this",
		message.BinaryContent{Path: "paste_1.txt", MIMEType: "text/plain", Data: []byte(content)})

	require.Equal(t, []string{"see this", "l1", "l2", "l3", "l10", "l11", "l12"},
		nonEmptyLinesWithoutRule(lines), "the middle lines must be gone")
	rule := ruleLines(lines)
	require.Len(t, rule, 1, "exactly one rule line")
	require.True(t, strings.HasPrefix(rule[0], "──"), "the rule renders as a rule line, got %q", rule[0])
}

// TestUserMessageNonTextAttachmentTags pin the tag shape: non-text
// attachments render as numbered tags minted by FormatRef, counting
// across the message.
func TestUserMessageNonTextAttachmentTags(t *testing.T) {
	t.Parallel()

	lines := attachmentLines(t, "",
		message.BinaryContent{Path: "paste_1.png", MIMEType: "image/png", Data: []byte("png")},
		message.BinaryContent{Path: "paste_2.pdf", MIMEType: "application/pdf", Data: []byte("pdf")},
		message.BinaryContent{Path: "paste_3.png", MIMEType: "image/png", Data: []byte("png")},
	)
	require.Equal(t, []string{"[Image #1] [File #2] [Image #3]"}, nonEmptyLines(lines))
}

// nonEmptyLines drops empty lines.
func nonEmptyLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// nonEmptyLinesWithoutRule drops empty lines and rule lines, so tests
// can assert the surviving content around them.
func nonEmptyLinesWithoutRule(lines []string) []string {
	var out []string
	for _, l := range lines {
		if l != "" && !strings.HasPrefix(l, "─") {
			out = append(out, l)
		}
	}
	return out
}

// ruleLines returns the rule lines among the rendered lines.
func ruleLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "─") {
			out = append(out, l)
		}
	}
	return out
}
