package chat

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/anim"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// assistantMessageTruncateFormat is the text shown when an assistant message is
// truncated in the collapsed state.
const assistantMessageTruncateFormat = "… (%d lines hidden) [click or space to expand]"

// assistantMessageTailWindowFormat is shown above a tail-windowed thinking
// block to advertise that earlier lines exist and that the user can
// promote the view to a full expansion. The promotion is wired through
// the existing ToggleExpanded path (click / space) — F5 deliberately
// does not add a new keybinding.
const assistantMessageTailWindowFormat = "… %d earlier lines hidden [click or space for full view]"

// maxCollapsedThinkingHeight defines the maximum height of the thinking
const maxCollapsedThinkingHeight = 10

// Default copy for a provider-refusal banner. The agent persists only
// the FinishReasonContentFilter reason; the TUI owns this text and
// fills it in when the finish part carries no message/details (the
// normal live path, and restored sessions). Kept here as the single
// source of truth so the render path and tests cannot drift apart.
const (
	refusalTagLabel = "REFUSED"
	refusalTitle    = "Model refused to continue"
	refusalDetails  = "The provider's safety classifier stopped this response before any usable content was produced. Rephrase the request, start a fresh session, or try a different model."
)

// maxExpandedThinkingTailLines is the F5 tail-window cap. When the user
// expands a thinking block whose post-glamour line count exceeds this
// threshold, only the last N lines are shown with an affordance line
// indicating how many earlier lines are hidden. Clicking / pressing
// space again promotes the view to a full expansion. The slice is
// taken AFTER glamour render (not before) so fenced code blocks,
// lists, and tables are not torn at arbitrary boundaries.
const maxExpandedThinkingTailLines = 200

// thinkingViewMode is the F5 three-state view machine for the thinking
// block. ToggleExpanded cycles
// collapsed → tail-window → full-expanded → collapsed, skipping the
// tail-window step when the rendered thinking fits within the cap so
// short blocks still toggle in two clicks.
type thinkingViewMode uint8

const (
	thinkingCollapsed thinkingViewMode = iota
	thinkingTailWindow
	thinkingFullExpanded
)

// assistantSection is a per-section render cache for AssistantMessageItem.
// Each section (thinking, content, error) carries its own keys so that
// streaming a section does not invalidate a different — often more
// expensive — section's cached render. srcHash is an FNV-64 of the
// section's source text; extra captures any other state that changes
// the rendered output (e.g. thinkingExpanded, the thinking footer
// inputs). valid disambiguates a real cache hit from the zero value
// when both source text and extras hash to zero. aux carries any
// per-section side data that the caller needs to recover on a hit
// (e.g. the thinking box height for click detection).
type assistantSection struct {
	width   int
	srcHash uint64
	extra   uint64
	out     string
	h       int
	aux     int
	valid   bool
}

// hit reports whether the cache entry matches the requested key.
func (s *assistantSection) hit(width int, srcHash, extra uint64) bool {
	return s.valid && s.width == width && s.srcHash == srcHash && s.extra == extra
}

// store records the rendered output under the given key.
func (s *assistantSection) store(width int, srcHash, extra uint64, out string, aux int) {
	s.width = width
	s.srcHash = srcHash
	s.extra = extra
	s.out = out
	s.h = lipgloss.Height(out)
	s.aux = aux
	s.valid = true
}

// reset drops the cached output.
func (s *assistantSection) reset() {
	*s = assistantSection{}
}

// fnv64 hashes a single string with FNV-64.
func fnv64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// incrementalHash keeps the FNV-64a hash of a string that grows by
// appending, so a section that is re-keyed on every animation frame is
// not re-hashed from the start each time — which would be quadratic in
// the length of a streaming response.
//
// sample holds a short prefix of the hashed text. That is how an append
// is told apart from a rewrite (a user retry restarts the text from
// scratch) without re-reading the whole thing. See CHARM-1785.
type incrementalHash struct {
	hash   uint64
	length int
	sample string
}

// hashSampleLen is how much of the hashed text is kept to detect a
// rewrite. Long enough that two different responses are very unlikely
// to share it, short enough to compare for free.
const hashSampleLen = 64

// sum returns the FNV-64a hash of s. It continues from the saved state
// when s extends the text hashed last time, and re-hashes in full when
// s shrank or diverged from it.
func (h *incrementalHash) sum(s string) uint64 {
	const fnvPrime64 = 1099511628211

	sampleLen := min(len(s), hashSampleLen)
	if h.length > 0 && len(s) >= h.length && s[:sampleLen] == h.sample {
		sum := h.hash
		for i := h.length; i < len(s); i++ {
			sum ^= uint64(s[i])
			sum *= fnvPrime64
		}
		h.hash = sum
		h.length = len(s)
		return sum
	}

	// First call, or the text diverged or shrank.
	sum := fnv64(s)
	h.hash = sum
	h.length = len(s)
	h.sample = s[:sampleLen]
	return sum
}

// reset drops the saved state so the next sum re-hashes in full.
func (h *incrementalHash) reset() {
	*h = incrementalHash{}
}

// countLines returns the number of lines in s (i.e. the number of
// newline-separated segments). Equivalent to len(strings.Split(s,
// "\n")) but allocates nothing. See CHARM-1785.
func countLines(s string) int {
	if s == "" {
		return 1
	}
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}

// tailLines returns the last n lines of s and the count of hidden
// (earlier) lines. totalLines is the pre-computed line count of s
// (from countLines). It finds the cut point with a bounded backward
// scan so the cost is O(n) in the number of kept lines, not O(L)
// in the total document length. See CHARM-1785.
func tailLines(s string, n, totalLines int) (tail string, hidden int) {
	if n <= 0 {
		return "", totalLines
	}
	if totalLines <= n {
		return s, 0
	}
	// Find the nth newline from the end. The tail starts after it.
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count == n {
				return s[i+1:], totalLines - n
			}
		}
	}
	return s, 0
}

// fnvFields hashes a list of byte fields with length-prefix framing
// so that no concatenation collision can occur between distinct
// field tuples (a NUL inside one field cannot impersonate a
// boundary between two fields). Each field is preceded by its
// length encoded as 8 bytes little-endian.
func fnvFields(fields ...[]byte) uint64 {
	h := fnv.New64a()
	var lenBuf [8]byte
	for _, f := range fields {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(f)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(f)
	}
	return h.Sum64()
}

// AssistantMessageItem represents an assistant message in the chat UI.
//
// This item includes thinking, and the content but does not include the tool calls.
type AssistantMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	message           *message.Message
	sty               *styles.Styles
	anim              *anim.Anim
	thinkingViewMode  thinkingViewMode
	thinkingBoxHeight int // Tracks the rendered thinking box height for click detection.

	// Incremental FNV-64a hashes of the two sections that grow while
	// the turn streams. Both are re-keyed on every animation frame, so
	// hashing them from the start each time is quadratic in the length
	// of the response. See CHARM-1785.
	thinkingHash incrementalHash
	contentHash  incrementalHash

	// Per-section render caches. Splitting these out means content
	// streaming does not invalidate the (often expensive) thinking
	// render, and vice versa.
	thinkingSec assistantSection
	contentSec  assistantSection
	errorSec    assistantSection

	// streamingContent caches a "stable prefix" glamour render of
	// the assistant content body so each streaming flush only
	// re-renders the trailing partial. F8 of
	// docs/notes/2026-05-12-chat-rendering-perf.md. See
	// streaming_markdown.go for the full algorithm.
	streamingContent streamingMarkdown

	// streamingThinking applies the same stable-prefix caching to
	// the thinking/reasoning section. Without this, every streaming
	// delta forces a full glamour re-render of the entire accumulated
	// thinking text, which burns CPU and starves the terminal emulator
	// during long reasoning traces.
	streamingThinking streamingMarkdown

	// animLabel is the label currently set on the spinner. Setting one
	// re-renders it rune by rune, so the label is only pushed when it
	// actually changes rather than on every animation frame.
	animLabel string
}

var (
	_ Expandable     = (*AssistantMessageItem)(nil)
	_ WorkingSpinner = (*AssistantMessageItem)(nil)
)

// NewAssistantMessageItem creates a new AssistantMessageItem.
func NewAssistantMessageItem(sty *styles.Styles, message *message.Message) MessageItem {
	v := list.NewVersioned()
	a := &AssistantMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        newCachedMessageItem(v),
		focusableMessageItem:     newFocusableMessageItem(v),
		message:                  message,
		sty:                      sty,
	}

	// Summary messages are generated outside a regular turn, so the turn
	// timer is not active while they stream; give them their own elapsed
	// timer anchored at item creation instead.
	suffix := func() string { return common.Elapsed() }
	if message.IsSummaryMessage {
		startedAt := time.Now()
		suffix = func() string { return common.FormatDuration(time.Since(startedAt)) }
	}

	a.anim = anim.New(anim.Settings{
		ID:          a.ID(),
		PulseGlyphs: anim.DefaultPulseGlyphs,
		GradColorA:  sty.WorkingGradFromColor,
		GradColorB:  sty.WorkingGradToColor,
		LabelColor:  sty.WorkingLabelColor,
		Suffix:      suffix,
		SuffixColor: sty.WorkingTimerColor,
	})
	return a
}

// Spinning implements [Animatable].
func (a *AssistantMessageItem) Spinning() bool {
	return a.isSpinning()
}

// Advance implements [Animatable].
func (a *AssistantMessageItem) Advance() bool {
	if !a.isSpinning() || !a.anim.Advance() {
		return false
	}
	// Bump the F6 list-cache version so the next draw re-renders
	// this item: a spinner frame mutates anim's internal counter,
	// which changes the rendered output but is invisible to the
	// per-section content hashes. Without the bump the list cache
	// would serve the previously rendered frame indefinitely and
	// the spinner would appear frozen.
	a.Bump()
	return true
}

// ID implements MessageItem.
func (a *AssistantMessageItem) ID() string {
	return a.message.ID
}

// RawRender implements [MessageItem].
func (a *AssistantMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	var spinner string
	if a.isSpinning() {
		spinner = a.renderSpinning()
	}

	content, height := a.renderMessageContent(cappedWidth)
	highlightedContent := a.renderHighlighted(content, cappedWidth, height)
	if spinner != "" {
		if highlightedContent != "" {
			highlightedContent += "\n\n"
		}
		return highlightedContent + spinner
	}

	return highlightedContent
}

// Render implements MessageItem.
func (a *AssistantMessageItem) Render(width int) string {
	// XXX: Here, we're manually applying the focused/blurred styles because
	// using lipgloss.Render can degrade performance for long messages due to
	// it's wrapping logic.
	// We already know that the content is wrapped to the correct width in
	// RawRender, so we can just apply the styles directly to each line.
	//
	// The split + per-line prefix loop is O(L); cache the result keyed
	// by (width, focused, sectionsFingerprint) so steady-state Render
	// becomes a pointer return. The sectionsFingerprint folds in the
	// per-section srcHash/extra so that any sub-cache change
	// invalidates this prefix cache without requiring an explicit
	// drop. Bypass the cache while spinning (RawRender's spinner
	// suffix changes every animation frame) or while a highlight
	// range is active (selection drag).
	useCache := !a.isSpinning() && !a.isHighlighted()
	cappedWidth := cappedMessageWidth(width)
	key := a.prefixCacheKey(cappedWidth)
	if useCache {
		if cached, ok := a.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	focused := a.sty.Messages.AssistantFocused.Render()
	blurred := a.sty.Messages.AssistantBlurred.Render()
	rendered := a.RawRender(width)
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		if a.focused {
			lines[i] = focused + line
		} else {
			lines[i] = blurred + line
		}
	}
	out := strings.Join(lines, "\n")
	if useCache {
		a.setCachedPrefixedRender(out, width, key)
	}
	return out
}

// prefixCacheKey builds the F3 prefixed-render cache key. We pack the
// focus bit into bit 0 and a fingerprint of the section caches into
// the upper bits, so any change to a sub-section's source text or
// extras forces the prefix cache to miss without needing an explicit
// drop. cappedWidth is included so a cached prefix never survives a
// section-cache miss caused by a width change. The finish reason is
// folded in too because it controls the composition of
// renderMessageContent (e.g. appending the constant "Canceled"
// string) — that decision lives outside any section's own hash.
func (a *AssistantMessageItem) prefixCacheKey(cappedWidth int) uint64 {
	thinkSrc, thinkExtra := a.thinkingKey()
	contentSrc, contentExtra := a.contentKey()
	errSrc, errExtra := a.errorKey()
	h := fnv.New64a()
	var buf [8]byte
	writeU64 := func(v uint64) {
		for i := range 8 {
			buf[i] = byte(v >> (8 * i))
		}
		_, _ = h.Write(buf[:])
	}
	writeU64(uint64(cappedWidth))
	writeU64(thinkSrc)
	writeU64(thinkExtra)
	writeU64(contentSrc)
	writeU64(contentExtra)
	writeU64(errSrc)
	writeU64(errExtra)
	writeU64(a.compositionKey())
	fingerprint := h.Sum64()
	var focusBit uint64
	if a.focused {
		focusBit = 1
	}
	return (fingerprint &^ 1) | focusBit
}

// compositionKey hashes the inputs to renderMessageContent's structural
// decisions (which sections to include, whether to append the
// constant "Canceled" footer) so that flipping IsFinished or the
// finish reason invalidates the prefix cache even when no section's
// own source text changed.
func (a *AssistantMessageItem) compositionKey() uint64 {
	var finishedFlag byte
	var reason string
	if a.message.IsFinished() {
		finishedFlag = 1
		reason = string(a.message.FinishReason())
	}
	// Length-prefixed framing keeps the finished flag and the reason
	// string from blending into one another.
	return fnvFields([]byte{finishedFlag}, []byte(reason))
}

// renderMessageContent renders the message content including thinking, main
// content, and finish reason. Each section is served from its own cache;
// only the section whose source text or extras changed since the last
// render is recomputed.
func (a *AssistantMessageItem) renderMessageContent(width int) (string, int) {
	var messageParts []string
	thinking := strings.TrimSpace(a.message.ReasoningContent().Thinking)
	if HideThinking {
		thinking = ""
	}
	content := strings.TrimSpace(a.message.Content().Text)

	if thinking != "" {
		messageParts = append(messageParts, a.cachedThinking(width))
	}

	if content != "" {
		if thinking != "" {
			messageParts = append(messageParts, "")
		}
		messageParts = append(messageParts, a.cachedContent(width))
	}

	if a.message.IsFinished() {
		switch {
		case a.message.FinishReason() == message.FinishReasonCanceled:
			messageParts = append(messageParts, a.sty.Messages.AssistantCanceled.Render("Canceled"))
		case a.message.IsErrorLike():
			messageParts = append(messageParts, a.cachedError(width))
		}
	}

	out := strings.Join(messageParts, "\n")
	return out, lipgloss.Height(out)
}

// thinkingKey returns the (srcHash, extra) cache key components for the
// thinking section. extra folds in everything other than the raw
// thinking text that affects the rendered output: the view mode
// (collapsed / tail-window / full) and the footer state (which
// depends on IsThinking, ToolCalls, and ThinkingDuration).
//
// The source hash is computed incrementally: during streaming the
// thinking text only grows by appending, so we continue the FNV-64a
// hash from the saved state rather than re-hashing the entire
// accumulated text. See CHARM-1785.
func (a *AssistantMessageItem) thinkingKey() (uint64, uint64) {
	thinking := a.message.ReasoningContent().Thinking
	srcHash := a.thinkingHash.sum(thinking)

	showFooter := !a.message.IsThinking() || len(a.message.ToolCalls()) > 0
	var durationStr string
	if showFooter {
		duration := a.message.ThinkingDuration()
		if duration.String() != "0s" {
			durationStr = duration.String()
		}
	}
	var footer byte
	if showFooter {
		footer = 1
	}
	// Length-prefixed framing avoids any delimiter collision between
	// the flag bytes and the duration string. The view mode is folded
	// in so that toggling collapsed ↔ tail-window ↔ full invalidates
	// only the thinking section, not content/error.
	extra := fnvFields([]byte{byte(a.thinkingViewMode), footer}, []byte(durationStr))
	return srcHash, extra
}

// contentKey returns the (srcHash, extra) cache key components for the
// main content section.
func (a *AssistantMessageItem) contentKey() (uint64, uint64) {
	return a.contentHash.sum(a.message.Content().Text), 0
}

// errorKey returns the (srcHash, extra) cache key components for the
// error / refusal section. Returns (0, 0) when no error-like finish
// is present so the cache stays a no-op for normal messages.
func (a *AssistantMessageItem) errorKey() (uint64, uint64) { //nolint:unparam // same shape as thinkingKey and contentKey
	if !a.message.IsFinished() || !a.message.IsErrorLike() {
		return 0, 0
	}
	finishPart := a.message.FinishPart()
	if finishPart == nil {
		return 0, 0
	}
	// Length-prefixed framing prevents Message+Details collisions
	// between distinct (Message, Details) tuples that would
	// otherwise concatenate to the same byte sequence. Fold the
	// reason in so ERROR vs REFUSED banners never share a cache slot.
	return fnvFields([]byte(finishPart.Reason), []byte(finishPart.Message), []byte(finishPart.Details)), 0
}

// cachedThinking returns the rendered thinking section, computing and
// caching it on miss. The thinking-box height (used for click target
// detection) is preserved across hits via assistantSection.aux so the
// cached path never desyncs click detection.
func (a *AssistantMessageItem) cachedThinking(width int) string {
	srcHash, extra := a.thinkingKey()
	if a.thinkingSec.hit(width, srcHash, extra) {
		a.thinkingBoxHeight = a.thinkingSec.aux
		return a.thinkingSec.out
	}
	out := a.renderThinking(a.message.ReasoningContent().Thinking, width)
	a.thinkingSec.store(width, srcHash, extra, out, a.thinkingBoxHeight)
	return out
}

// cachedContent returns the rendered content section.
func (a *AssistantMessageItem) cachedContent(width int) string {
	srcHash, extra := a.contentKey()
	if a.contentSec.hit(width, srcHash, extra) {
		return a.contentSec.out
	}
	out := a.renderMarkdown(a.message.Content().Text, width)
	a.contentSec.store(width, srcHash, extra, out, 0)
	return out
}

// cachedError returns the rendered error section.
func (a *AssistantMessageItem) cachedError(width int) string {
	srcHash, extra := a.errorKey()
	if a.errorSec.hit(width, srcHash, extra) {
		return a.errorSec.out
	}
	out := a.renderError(width)
	a.errorSec.store(width, srcHash, extra, out, 0)
	return out
}

// renderThinking renders the thinking/reasoning content with footer.
//
// Slicing happens AFTER glamour rendering so fenced code blocks, list
// continuations, and tables are not split mid-block — the same
// boundary problem §4.4 of the design note flags. The bordered
// ThinkingBox style is applied on top of the (already-windowed)
// lines so the visual box matches what the user sees today.
func (a *AssistantMessageItem) renderThinking(thinking string, width int) string {
	renderer := common.QuietMarkdownRenderer(a.sty, width)
	rendered := a.streamingThinking.Render(thinking, width, renderer)
	rendered = strings.TrimSpace(rendered)
	// The renderer already knows this count from its cached prefix, so
	// take it rather than rescanning a document that only grows.
	renderedLines := a.streamingThinking.LastLines()

	// Count lines and, for the windowed view modes, slice the tail
	// WITHOUT splitting the entire rendered document. Splitting a
	// 1200-line render just to keep the last 10 lines is O(n) per
	// tick; tailLines finds the cut point with a bounded backward
	// scan. See CHARM-1785.
	var lines []string
	var totalLines int
	switch a.thinkingViewMode {
	case thinkingCollapsed:
		totalLines = renderedLines
		if totalLines > maxCollapsedThinkingHeight {
			tail, hidden := tailLines(rendered, maxCollapsedThinkingHeight, totalLines)
			hint := a.sty.Messages.ThinkingTruncationHint.Render(
				fmt.Sprintf(assistantMessageTruncateFormat, hidden),
			)
			lines = append([]string{hint, ""}, strings.Split(tail, "\n")...)
		} else {
			lines = strings.Split(rendered, "\n")
		}
	case thinkingTailWindow:
		totalLines = renderedLines
		if totalLines > maxExpandedThinkingTailLines {
			tail, hidden := tailLines(rendered, maxExpandedThinkingTailLines, totalLines)
			hint := a.sty.Messages.ThinkingTruncationHint.Render(
				fmt.Sprintf(assistantMessageTailWindowFormat, hidden),
			)
			lines = append([]string{hint, ""}, strings.Split(tail, "\n")...)
		} else {
			lines = strings.Split(rendered, "\n")
		}
	default:
		lines = strings.Split(rendered, "\n")
	}

	thinkingStyle := a.sty.Messages.ThinkingBox.Width(width)
	result := thinkingStyle.Render(strings.Join(lines, "\n"))
	a.thinkingBoxHeight = lipgloss.Height(result)

	var footer string
	// if thinking is done add the thought for footer
	if !a.message.IsThinking() || len(a.message.ToolCalls()) > 0 {
		duration := a.message.ThinkingDuration()
		if duration.String() != "0s" {
			footer = a.sty.Messages.ThinkingFooterTitle.Render("Thought for ") +
				a.sty.Messages.ThinkingFooterDuration.Render(duration.String())
		}
	}

	if footer != "" {
		result += "\n\n" + footer
	}

	return result
}

// renderMarkdown renders content as markdown. F8 routes the call
// through streamingContent, which caches the glamour render of a
// "stable prefix" so each streaming flush only re-renders the
// trailing partial. The streaming cache invalidates itself on
// width change and on any content that is not a prefix-extension
// of the previously rendered content (e.g. user retried the
// turn), and falls back to a full render whenever boundary
// detection has the slightest doubt — see
// findSafeMarkdownBoundary.
func (a *AssistantMessageItem) renderMarkdown(content string, width int) string {
	renderer := common.MarkdownRenderer(a.sty, width)
	return a.streamingContent.Render(content, width, renderer)
}

func (a *AssistantMessageItem) renderSpinning() string {
	// This spinner runs from the moment the request goes out, which includes
	// the stretch before any reasoning or content comes back. Label that
	// stretch too: an unlabeled spinner under a finished tool call reads as
	// the tool still running, when what is actually happening is the model
	// reading its output.
	label := "Thinking"
	if a.message.IsSummaryMessage {
		label = "Summarizing"
	}
	if a.animLabel != label {
		a.animLabel = label
		a.anim.SetLabel(label)
	}
	return a.anim.Render()
}

// renderError renders an error or provider-refusal banner.
func (a *AssistantMessageItem) renderError(width int) string {
	finishPart := a.message.FinishPart()
	tagLabel := "ERROR"
	titleText := finishPart.Message
	detailsText := finishPart.Details
	if finishPart.Reason == message.FinishReasonContentFilter {
		tagLabel = refusalTagLabel
		titleText = cmp.Or(titleText, refusalTitle)
		detailsText = cmp.Or(detailsText, refusalDetails)
	}
	errTag := a.sty.Messages.ErrorTag.Render(tagLabel)
	truncated := ansi.Truncate(titleText, width-2-lipgloss.Width(errTag), "…")
	title := fmt.Sprintf("%s %s", errTag, a.sty.Messages.ErrorTitle.Render(truncated))
	if detailsText == "" {
		return title
	}
	details := a.sty.Messages.ErrorDetails.Width(width - 2).Render(detailsText)
	return fmt.Sprintf("%s\n\n%s", title, details)
}

// assistantSpinnerActive reports whether an assistant message in this
// state renders the working animation: the turn is still running and
// nothing readable (content, tool calls) has arrived yet. The spinner
// is the only thing such a message can show, so it is also the only
// reason to keep the message's transcript item alive.
func assistantSpinnerActive(msg *message.Message) bool {
	if strings.TrimSpace(msg.Content().Text) != "" || len(msg.ToolCalls()) > 0 {
		return false
	}
	return msg.IsThinking() || !msg.IsFinished()
}

// isSpinning returns true if the assistant message is still generating.
func (a *AssistantMessageItem) isSpinning() bool {
	return assistantSpinnerActive(a.message)
}

// SpinnerOnly implements [WorkingSpinner]. It reports whether this item
// currently renders nothing but the working animation: no reasoning, no
// content, no error banner. isSpinning already rules out content and
// tool calls, and an error banner only renders on a finished message,
// which never spins — so the reasoning text is all that is left to
// check.
func (a *AssistantMessageItem) SpinnerOnly() bool {
	if !a.isSpinning() {
		return false
	}
	if HideThinking {
		return true
	}
	return strings.TrimSpace(a.message.ReasoningContent().Thinking) == ""
}

// IsLiveThinking reports whether the item is the live thinking entry:
// the message is still streaming reasoning and no content has arrived
// yet. The entry is transient, so while this is true there is nothing
// settled to select or copy.
func (a *AssistantMessageItem) IsLiveThinking() bool {
	return a.message.IsThinking()
}

// SetMessage is used to update the underlying message. Only the
// sub-section caches whose source text or extras changed are
// invalidated; the others survive and serve cache hits on the next
// RawRender.
func (a *AssistantMessageItem) SetMessage(msg *message.Message) {
	a.message = msg
	// Bump the F6 version even if the underlying *message.Message
	// pointer is identical: callers may have mutated the message in
	// place (delta append) and we cannot tell from here. The
	// per-section caches dedupe identical content via FNV-64 hashes,
	// so a redundant bump only costs one list-cache repopulation.
	a.Bump()
	// The prefix cache is keyed by a fingerprint that includes every
	// section's source hash, so an unchanged section keeps its prefix
	// cache valid while a changed section forces a miss naturally.
	// Section caches themselves are content-keyed, so they do not
	// need an explicit drop here either. If the message started
	// spinning the UI's animation clock picks it up on the next
	// update.
}

// Finished implements list.Item. The assistant message is freezable
// once the message reports IsFinished() and is no longer spinning
// (no animation frame remains pending). Streaming tail animation is
// caught by isSpinning, so freezing only kicks in once the turn is
// fully terminal. The list cache invalidates the entry on the next
// version bump if anything (focus, highlight, expansion) changes.
func (a *AssistantMessageItem) Finished() bool {
	return a.message.IsFinished() && !a.isSpinning()
}

// clearCache drops every cached render for this item, including the
// per-section caches. Shadows the embedded cachedMessageItem.clearCache
// so ClearItemCaches (style change) wipes the section caches too.
// F8: also drop the streaming-markdown stable-prefix cache because
// the cached glamour render embeds the OLD style's ANSI sequences
// and is no longer visually consistent with the new style.
func (a *AssistantMessageItem) clearCache() {
	a.cachedMessageItem.clearCache()
	a.thinkingSec.reset()
	a.contentSec.reset()
	a.errorSec.reset()
	a.streamingContent.Reset()
	a.streamingThinking.Reset()
	a.thinkingHash.reset()
	a.contentHash.reset()
}

// ToggleExpanded advances the F5 thinking view-mode cycle and returns
// whether the item is now in any expanded state (tail-window or full).
// The cycle is collapsed → tail-window → full → collapsed, with the
// tail-window step skipped when the rendered thinking fits within
// maxExpandedThinkingTailLines so short blocks remain a two-click
// toggle.
func (a *AssistantMessageItem) ToggleExpanded() bool {
	next := thinkingCollapsed
	switch a.thinkingViewMode {
	case thinkingCollapsed:
		next = thinkingFullExpanded
		if a.tailWindowWouldTruncate() {
			next = thinkingTailWindow
		}
	case thinkingTailWindow:
		next = thinkingFullExpanded
	}
	a.SetExpansionLevel(uint8(next))
	return a.thinkingViewMode != thinkingCollapsed
}

// ExpansionLevel implements [Expandable]: the thinking view mode.
func (a *AssistantMessageItem) ExpansionLevel() uint8 {
	return uint8(a.thinkingViewMode)
}

// SetExpansionLevel implements [Expandable]. Both the thinking section
// cache and the F3 prefix cache fold thinkingViewMode into their keys,
// so only the streaming prefix cache needs dropping here.
//
// When the message carries no thinking text the level is left alone:
// there is nothing to expand, and mutating the view mode would thrash
// the thinking-section cache key for no visible benefit.
func (a *AssistantMessageItem) SetExpansionLevel(level uint8) {
	mode := thinkingViewMode(level)
	if mode > thinkingFullExpanded || mode == a.thinkingViewMode ||
		strings.TrimSpace(a.message.ReasoningContent().Thinking) == "" {
		return
	}
	a.thinkingViewMode = mode
	// View-mode changes alter the windowing slice applied after
	// glamour render. The streaming prefix cache may have been
	// seeded under a different slice regime, and glued renders are
	// not byte-identical to monolithic ones. Drop the prefix cache
	// so the next render is clean.
	a.streamingThinking.Reset()
	a.Bump()
}

// tailWindowWouldTruncate reports whether the current thinking text
// is long enough that the tail-window step is worth inserting into
// the toggle cycle. We use a cheap source-text logical-line count
// as the heuristic rather than peeking into the cache: the cache
// may be populated in collapsed state (where its height is bounded
// by maxCollapsedThinkingHeight and tells us nothing about the
// underlying length), and re-running glamour just to count lines
// would defeat the cache. The heuristic can over-trigger (a source
// with many short lines may wrap to fewer than N lines), in which
// case the tail-window render is visually identical to full and
// the cycle costs the user one extra toggle — preferred over the
// alternative of failing to show the affordance on a genuinely
// long block.
//
// Logical line count is `1 + newlineCount` (a string with no
// newlines is one line). Comparing newline count alone introduced
// an off-by-one that let a source whose post-newline-split length
// equalled the cap skip the tail-window step.
func (a *AssistantMessageItem) tailWindowWouldTruncate() bool {
	lineCount := 1 + strings.Count(a.message.ReasoningContent().Thinking, "\n")
	return lineCount > maxExpandedThinkingTailLines
}

// HandleMouseClick implements MouseClickable. It signals (via a true return)
// that the click lies on the thinking box so the caller can invoke
// [AssistantMessageItem.ToggleExpanded] through the generic [Expandable]
// path. Toggling here directly would double-toggle because the caller always
// runs the generic path after a handled click.
func (a *AssistantMessageItem) HandleMouseClick(btn ansi.MouseButton, x, y int) bool {
	if btn != ansi.MouseLeft {
		return false
	}
	// Only the thinking box is clickable; other regions of the assistant
	// message should not trigger expansion.
	return a.thinkingBoxHeight > 0 && y < a.thinkingBoxHeight
}

// HandleKeyEvent implements KeyEventHandler.
func (a *AssistantMessageItem) HandleKeyEvent(msg tea.KeyMsg, keys ItemKeymap) (bool, tea.Cmd) {
	if keys.MatchesCopy(msg) {
		return true, common.CopyToClipboard(copyMessageText(a.message), copyToastMessage)
	}
	return false, nil
}
