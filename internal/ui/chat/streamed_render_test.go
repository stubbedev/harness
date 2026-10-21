package chat

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func gluedReference(prefix, trail string) string {
	switch {
	case prefix == "":
		return trail
	case trail == "":
		return prefix
	default:
		return prefix + "\n\n" + trail
	}
}

func streamedRenderCases() []streamedRender {
	fragments := []string{
		"",
		"a",
		"one\ntwo",
		"one\ntwo\nthree\nfour\nfive",
		"\r",
		"\u00a0x\u00a0",
		"x\n\n\ny",
		strings.Repeat("line\n", 30) + "end",
	}
	var out []streamedRender
	for _, p := range fragments {
		for _, tr := range fragments {
			out = append(out, streamedRender{prefix: p, trail: tr})
		}
	}
	return out
}

func TestStreamedRenderMatchesGluedString(t *testing.T) {
	t.Parallel()

	for _, r := range streamedRenderCases() {
		glued := gluedReference(r.prefix, r.trail)
		r.lines = countLines(glued)
		name := fmt.Sprintf("%q|%q", r.prefix, r.trail)

		require.Equal(t, glued, r.String(), name)
		require.Equal(t, len(glued), r.Len(), name)

		var b strings.Builder
		r.appendTo(&b)
		require.Equal(t, glued, b.String(), name)

		require.Equal(t, strings.TrimSpace(glued), r.trimSpace().String(), name)

		for n := range r.lines + 3 {
			wantTail, wantHidden := tailLines(glued, n, r.lines)
			gotTail, gotHidden := r.tail(n)
			require.Equal(t, wantTail, gotTail, "%s n=%d", name, n)
			require.Equal(t, wantHidden, gotHidden, "%s n=%d", name, n)
		}
	}
}

func TestStreamingMarkdownPrefixIsAViewOfTheCache(t *testing.T) {
	t.Parallel()

	const width = 61
	r := newTestRenderer(t, width)
	var sm streamingMarkdown
	doc := strings.Repeat("A paragraph of reasoning that runs on for a while.\n\n", 60)
	var advanced int
	for _, p := range progressivePrefixes(doc, 40) {
		if p == "" {
			continue
		}
		got := sm.Render(p, width, r)
		cached := sm.stablePrefixRender.String()
		if got.prefix == "" {
			continue
		}
		advanced++
		require.Equal(t, cached, got.prefix)
		require.Same(t, unsafe.StringData(cached), unsafe.StringData(got.prefix),
			"the stable prefix must be handed out without copying it")
	}
	require.Positive(t, advanced)
}

func TestStreamedRenderTailDoesNotCopyThePrefix(t *testing.T) {
	prefix := strings.Repeat("stable line\n", 10000) + "last stable"
	r := streamedRender{prefix: prefix, trail: "a\nb\nc"}
	r.lines = countLines(r.String())

	tail, hidden := r.tail(maxCollapsedThinkingHeight)
	require.Equal(t, r.lines-maxCollapsedThinkingHeight, hidden)
	require.Less(t, len(tail), 200)

	allocated := testing.AllocsPerRun(10, func() {
		_, _ = r.tail(maxCollapsedThinkingHeight)
	})
	require.LessOrEqual(t, allocated, 1.0)
}

func TestPrefixLinesMatchesSplitJoin(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"", "a", "\n", "a\n", "\na", "a\nb\nc", "a\n\n\nb\n"} {
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			lines[i] = ">>" + line
		}
		require.Equal(t, strings.Join(lines, "\n"), prefixLines(s, ">>"), "%q", s)
	}
}

func TestPrefixLinesAllocatesOnce(t *testing.T) {
	body := strings.Repeat("some rendered line\n", 500)
	allocated := testing.AllocsPerRun(10, func() {
		_ = prefixLines(body, "\x1b[38;5;1m\x1b[0m")
	})
	require.Equal(t, 1.0, allocated)
}

func legacyThinkingBody(a *AssistantMessageItem, rendered string, total int) string {
	rendered = strings.TrimSpace(rendered)
	var limit int
	hint := truncationHint
	switch a.thinkingViewMode {
	case thinkingCollapsed:
		limit = maxCollapsedThinkingHeight
	case thinkingTailWindow:
		limit, hint = maxExpandedThinkingTailLines, tailWindowHint
	default:
		return rendered
	}
	if total <= limit {
		return rendered
	}
	tail, hidden := tailLines(rendered, limit, total)
	return a.sty.Messages.ThinkingTruncationHint.Render(hint(hidden)) + "\n\n" + tail
}

func TestRenderThinkingWindowMatchesFullDocumentSlice(t *testing.T) {
	t.Parallel()

	modes := []struct {
		mode  thinkingViewMode
		width int
	}{
		{thinkingCollapsed, 63},
		{thinkingTailWindow, 65},
		{thinkingFullExpanded, 67},
	}
	doc := buildThinkingBlock(60)
	for _, m := range modes {
		t.Run(fmt.Sprint(m.mode), func(t *testing.T) {
			t.Parallel()
			sty := styles.CharmtonePantera()
			item := NewAssistantMessageItem(&sty, &message.Message{ID: "t", Role: message.Assistant}).(*AssistantMessageItem)
			item.thinkingViewMode = m.mode
			var reference streamingMarkdown
			r := common.QuietMarkdownRenderer(&sty, m.width)
			for _, p := range progressivePrefixes(doc, 50) {
				if strings.TrimSpace(p) == "" {
					continue
				}
				ref := reference.Render(p, m.width, r)
				want := sty.Messages.ThinkingBox.Width(m.width).Render(legacyThinkingBody(item, ref.String(), ref.lines))
				got := item.renderThinking(p, m.width)
				require.Equal(t, want, got, "prefix len %d", len(p))
			}
		})
	}
}

func TestAssistantRenderMatchesPerLinePrefix(t *testing.T) {
	t.Parallel()

	const width = 69
	sty := styles.CharmtonePantera()
	msg := &message.Message{ID: "c", Role: message.Assistant}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	msg.AppendReasoningContent(buildThinkingBlock(3))
	for i := range 40 {
		msg.AppendContent(fmt.Sprintf("Paragraph %d of the answer.\n\n", i))
		item.SetMessage(msg)
		item.SetFocused(i%2 == 0)

		raw := item.RawRender(width)
		lines := strings.Split(raw, "\n")
		style := item.focusStyle()
		for j, line := range lines {
			lines[j] = style + line
		}
		require.Equal(t, strings.Join(lines, "\n"), item.Render(width), "step %d", i)
	}
}
