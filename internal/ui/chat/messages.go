package chat

import (
	"fmt"
	"image"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// MessageLeftPaddingTotal is the total width that is taken up by the border +
// padding. We also cap the width so text is readable to the maxTextWidth(120).
const MessageLeftPaddingTotal = 2

// toolNestIndent is the extra left column a tool call gains per level
// of nesting inside an expanded tool group, under the group's bar.
const toolNestIndent = 1

// toolNestIndentString is the per-level indent rendered in front of a
// nested tool call's lines.
var toolNestIndentString = strings.Repeat(" ", toolNestIndent)

// ToolBodyWidth returns the width a tool call's body renders at when
// its row is given width at the given nesting level. Level 0 is a
// top-level call, whose bar the call itself (or its singleton group)
// draws; each level below is a call nested inside an expanded group,
// which gains one indent column per level on top of the bar. Every
// tool body width - a call's own render and every group child's -
// derives here, so the chrome math cannot drift between call sites.
func ToolBodyWidth(width, level int) int {
	return max(width-MessageLeftPaddingTotal-level*toolNestIndent, 1)
}

// ViewportCol converts an item-content column into a viewport column.
// Mouse coordinates and the selection state built from them live in
// viewport space, where each rendered message string starts at column
// 0 with its border and padding inline; item content and the ranges
// items highlight start after that chrome. These two functions are the
// single definition of the relationship - every conversion flows
// through them, so the spaces cannot drift apart.
func ViewportCol(contentCol int) int {
	return contentCol + MessageLeftPaddingTotal
}

// ContentCol converts a viewport column into an item-content column,
// clamped at the content's left edge so a click on the chrome itself
// maps to the first content cell rather than wrapping negative.
func ContentCol(viewportCol int) int {
	return max(viewportCol-MessageLeftPaddingTotal, 0)
}

// maxTextWidth is the maximum width text messages can be
const maxTextWidth = 120

// Identifiable is an interface for items that can provide a unique identifier.
type Identifiable interface {
	ID() string
}

// Animatable is an interface for items that support animation. Items do
// not schedule their own frames: the UI runs a single animation clock and
// calls Advance on every visible item that reports Spinning.
type Animatable interface {
	// Spinning reports whether the item currently shows a running
	// animation and therefore needs clock ticks.
	Spinning() bool
	// Advance moves the animation forward by one frame and reports
	// whether the rendered output changed. Implementations must bump
	// their version when it did so the list cache re-renders the item.
	Advance() bool
}

// Expandable is an interface for items that can be expanded or collapsed.
type Expandable interface {
	// ToggleExpanded toggles the expanded state of the item. It returns
	// whether the item is now expanded.
	ToggleExpanded() bool
}

// ItemKeymap holds the key bindings chat items consult in HandleKeyEvent.
// The model layer owns them, so options.tui.keybinds overrides reach item
// actions too; items receive the bindings per event instead of hardcoding
// keys.
type ItemKeymap struct {
	Copy        key.Binding
	ScrollLeft  key.Binding
	ScrollRight key.Binding
}

// DefaultItemKeymap returns the item bindings matching the built-in keymap
// defaults, used until the model layer injects its (possibly rebound) ones.
func DefaultItemKeymap() ItemKeymap {
	km := keys.Active().Chat
	return ItemKeymap{
		Copy:        km.Copy,
		ScrollLeft:  km.ScrollLeft,
		ScrollRight: km.ScrollRight,
	}
}

// MatchesCopy reports whether msg triggers the copy action.
func (k ItemKeymap) MatchesCopy(msg tea.KeyMsg) bool {
	return key.Matches(msg, k.Copy)
}

// MatchesScrollLeft reports whether msg scrolls the item left.
func (k ItemKeymap) MatchesScrollLeft(msg tea.KeyMsg) bool {
	return key.Matches(msg, k.ScrollLeft)
}

// MatchesScrollRight reports whether msg scrolls the item right.
func (k ItemKeymap) MatchesScrollRight(msg tea.KeyMsg) bool {
	return key.Matches(msg, k.ScrollRight)
}

// KeyEventHandler is an interface for items that can handle key events.
type KeyEventHandler interface {
	HandleKeyEvent(msg tea.KeyMsg, keys ItemKeymap) (bool, tea.Cmd)
}

// MessageItem represents a [message.Message] item that can be displayed in the
// UI and be part of a [list.List] identifiable by a unique ID.
type MessageItem interface {
	list.Item
	list.RawRenderable
	Identifiable
}

// HighlightableMessageItem is a message item that supports highlighting.
type HighlightableMessageItem interface {
	MessageItem
	list.Highlightable
}

// WorkingSpinner is implemented by items that may, depending on how far
// the turn has got, render nothing but the working animation. Such an
// item is the turn's "still working" line rather than a message of its
// own: it holds nothing a reader would select or copy, and it must not
// come between a run of tool calls and the group they fold into.
type WorkingSpinner interface {
	MessageItem
	SpinnerOnly() bool
}

// IsWorkingSpinner reports whether the item currently renders nothing
// but the working animation.
func IsWorkingSpinner(item list.Item) bool {
	s, ok := item.(WorkingSpinner)
	return ok && s.SpinnerOnly()
}

// LiveThinker is implemented by items that may currently be the live
// thinking entry: an assistant message still streaming reasoning, with
// no settled content yet.
type LiveThinker interface {
	MessageItem
	IsLiveThinking() bool
}

// IsTransient reports whether the item is a transient indicator rather
// than settled content: the turn's working spinner or a message that
// is still thinking. Such items keep changing underneath the reader,
// so they hold nothing stable to select, focus, or copy.
func IsTransient(item list.Item) bool {
	if s, ok := item.(WorkingSpinner); ok && s.SpinnerOnly() {
		return true
	}
	t, ok := item.(LiveThinker)
	return ok && t.IsLiveThinking()
}

// FocusableMessageItem is a message item that supports focus.
type FocusableMessageItem interface {
	MessageItem
	list.Focusable
}

// SendMsg represents a message to send a chat message.
type SendMsg struct {
	Text        string
	Attachments []message.Attachment
}

type highlightableMessageItem struct {
	// version is the parent item's version counter. SetHighlight
	// bumps it on every observable change so the F6 list memo and
	// any frozen entry get invalidated when a selection drag enters
	// or leaves the item.
	version *list.Versioned

	startLine   int
	startCol    int
	endLine     int
	endCol      int
	highlighter list.Highlighter
}

var _ list.Highlightable = (*highlightableMessageItem)(nil)

// isHighlighted returns true if the item has a highlight range set.
func (h *highlightableMessageItem) isHighlighted() bool {
	return h.startLine != -1 || h.endLine != -1
}

// renderHighlighted highlights the content if necessary.
func (h *highlightableMessageItem) renderHighlighted(content string, width, height int) string {
	if !h.isHighlighted() {
		return content
	}
	area := image.Rect(0, 0, width, height)
	return list.Highlight(content, area, h.startLine, h.startCol, h.endLine, h.endCol, h.highlighter)
}

// SetHighlight implements [list.Highlightable]. Columns are in item
// content space, matching Highlight and renderHighlighted; the caller
// converts from viewport columns via ContentCol.
func (h *highlightableMessageItem) SetHighlight(startLine int, startCol int, endLine int, endCol int) {
	if h.startLine == startLine && h.startCol == startCol && h.endLine == endLine && h.endCol == endCol {
		return
	}
	h.startLine = startLine
	h.startCol = startCol
	h.endLine = endLine
	h.endCol = endCol
	if h.version != nil {
		h.version.Bump()
	}
}

// Highlight implements list.Highlightable.
func (h *highlightableMessageItem) Highlight() (startLine int, startCol int, endLine int, endCol int) {
	return h.startLine, h.startCol, h.endLine, h.endCol
}

func defaultHighlighter(sty *styles.Styles, v *list.Versioned) *highlightableMessageItem {
	return &highlightableMessageItem{
		version:     v,
		startLine:   -1,
		startCol:    -1,
		endLine:     -1,
		endCol:      -1,
		highlighter: list.ToHighlighter(sty.TextSelection),
	}
}

// cacheClearable is implemented by message items that cache rendered
// output and can be asked to drop the cache.
type cacheClearable interface {
	clearCache()
}

// ClearItemCaches drops any cached rendered output on each item so the
// next render uses the current styles. It also bumps each item's
// version so the F6 list-level memo invalidates frozen entries on
// the next render.
func ClearItemCaches(items []MessageItem) {
	for _, item := range items {
		if cc, ok := item.(cacheClearable); ok {
			cc.clearCache()
		}
		if v, ok := item.(interface{ Bump() }); ok {
			v.Bump()
		}
	}
}

// cachedMessageItem caches rendered message content to avoid re-rendering.
//
// This should be used by any message that can store a cached version of its render. e.x user,assistant... and so on
//
// THOUGHT(kujtim): we should consider if its efficient to store the render for different widths
// the issue with that could be memory usage
type cachedMessageItem struct {
	// ver is the owning item's version counter; invalidate bumps it so
	// dropping the render and announcing the change stay one step.
	ver *list.Versioned

	// rendered is the cached rendered string
	rendered string
	// width and height are the dimensions of the cached render
	width  int
	height int

	// prefixedRendered caches the per-line-prefixed Render output (the
	// result of splitting RawRender by newlines and prepending a focus
	// or selection prefix to every line). Items rebuild this every
	// frame today; caching it keyed by (prefixedWidth, prefixedKey)
	// turns Render into a pointer return when item state is stable.
	//
	// Invalidation lives in clearCache; callers must additionally
	// bypass this cache whenever the prefixed output would not be
	// stable (spinner ticks, active highlight ranges) by not calling
	// setCachedPrefixedRender for those frames.
	prefixedRendered string
	prefixedWidth    int
	prefixedKey      uint64
}

// getCachedRender returns the cached render if it exists for the given width.
func (c *cachedMessageItem) getCachedRender(width int) (string, int, bool) {
	if c.width == width && c.rendered != "" {
		return c.rendered, c.height, true
	}
	return "", 0, false
}

// setCachedRender sets the cached render.
func (c *cachedMessageItem) setCachedRender(rendered string, width, height int) {
	c.rendered = rendered
	c.width = width
	c.height = height
}

// getCachedPrefixedRender returns the cached prefixed render if it exists
// for the given (width, key). The key encodes any state that changes the
// per-line prefix (focused/blurred, compact, ...).
func (c *cachedMessageItem) getCachedPrefixedRender(width int, key uint64) (string, bool) {
	if c.prefixedRendered != "" && c.prefixedWidth == width && c.prefixedKey == key {
		return c.prefixedRendered, true
	}
	return "", false
}

// setCachedPrefixedRender stores the cached prefixed render.
func (c *cachedMessageItem) setCachedPrefixedRender(rendered string, width int, key uint64) {
	c.prefixedRendered = rendered
	c.prefixedWidth = width
	c.prefixedKey = key
}

// newCachedMessageItem returns an empty cache owned by the item whose
// version counter is ver.
func newCachedMessageItem(ver *list.Versioned) *cachedMessageItem {
	return &cachedMessageItem{ver: ver}
}

// invalidate drops the cached render and bumps the item's version: the
// one call a mutator that changes the rendered output makes.
func (c *cachedMessageItem) invalidate() {
	c.clearCache()
	c.ver.Bump()
}

// clearCache clears the cached render.
func (c *cachedMessageItem) clearCache() {
	c.rendered = ""
	c.width = 0
	c.height = 0
	c.prefixedRendered = ""
	c.prefixedWidth = 0
	c.prefixedKey = 0
}

// focusableMessageItem is a base struct for message items that can be focused.
type focusableMessageItem struct {
	// version is the parent item's version counter. SetFocused
	// bumps it whenever focus actually flips so the F6 list memo
	// invalidates the per-line focus prefix.
	version *list.Versioned
	focused bool
}

// newFocusableMessageItem returns a focusableMessageItem wired to the
// shared version counter.
func newFocusableMessageItem(v *list.Versioned) *focusableMessageItem {
	return &focusableMessageItem{version: v}
}

// SetFocused implements MessageItem.
func (f *focusableMessageItem) SetFocused(focused bool) {
	if f.focused == focused {
		return
	}
	f.focused = focused
	if f.version != nil {
		f.version.Bump()
	}
}

// AssistantInfoID returns a stable ID for assistant info items.
func AssistantInfoID(messageID string) string {
	return fmt.Sprintf("%s:assistant-info", messageID)
}

// ShouldShowAssistantInfo reports whether an assistant message should
// render its info footer. The turn that ends the prompt always gets one;
// intermediate turns only get one when a Prism-routed model name is
// available, since that is the only case where the footer adds
// per-turn information.
func ShouldShowAssistantInfo(msg *message.Message) bool {
	finishData := msg.FinishPart()
	if finishData == nil {
		return false
	}
	return finishData.Reason == message.FinishReasonEndTurn || msg.PrismModelName != ""
}

// AssistantInfoItem renders model info and response time after assistant completes.
type AssistantInfoItem struct {
	*list.Versioned
	*cachedMessageItem

	id                  string
	message             *message.Message
	sty                 *styles.Styles
	cfg                 *config.Config
	lastUserMessageTime time.Time
}

// NewAssistantInfoItem creates a new AssistantInfoItem.
func NewAssistantInfoItem(sty *styles.Styles, message *message.Message, cfg *config.Config, lastUserMessageTime time.Time) MessageItem {
	v := list.NewVersioned()
	return &AssistantInfoItem{
		Versioned:           v,
		cachedMessageItem:   newCachedMessageItem(v),
		id:                  AssistantInfoID(message.ID),
		message:             message,
		sty:                 sty,
		cfg:                 cfg,
		lastUserMessageTime: lastUserMessageTime,
	}
}

// Finished implements list.Item. Assistant info blocks render a fixed
// model/duration footer once the assistant turn finishes; the data
// is immutable after construction so the entry is safe to freeze.
func (a *AssistantInfoItem) Finished() bool {
	return true
}

// ID implements MessageItem.
func (a *AssistantInfoItem) ID() string {
	return a.id
}

// RawRender implements MessageItem.
func (a *AssistantInfoItem) RawRender(width int) string {
	innerWidth := max(0, width-MessageLeftPaddingTotal)
	content, _, ok := a.getCachedRender(innerWidth)
	if !ok {
		content = a.renderContent(innerWidth)
		height := lipgloss.Height(content)
		a.setCachedRender(content, innerWidth, height)
	}
	return content
}

// Render implements MessageItem.
func (a *AssistantInfoItem) Render(width int) string {
	// AssistantInfoItem uses a single, state-independent prefix; key 0
	// is sufficient. The cache is invalidated whenever the underlying
	// cachedMessageItem render is cleared.
	if cached, ok := a.getCachedPrefixedRender(width, 0); ok {
		return cached
	}
	prefix := a.sty.Messages.SectionHeader.Render()
	lines := strings.Split(a.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	a.setCachedPrefixedRender(out, width, 0)
	return out
}

func (a *AssistantInfoItem) renderContent(width int) string {
	finishData := a.message.FinishPart()
	if finishData == nil {
		return ""
	}
	// The final turn of a prompt keeps the full footer (duration and
	// separator line); intermediate turns render a compact header.
	isFinalTurn := finishData.Reason == message.FinishReasonEndTurn

	icon := a.sty.Messages.AssistantInfoIcon.Render(styles.ModelIcon)
	mainModelName := "Unknown Model"
	if model := a.cfg.GetModel(a.message.Provider, a.message.Model); model != nil {
		mainModelName = model.Name
	}
	modelFormatted := a.sty.Messages.AssistantInfoModel.Render(mainModelName)
	// A Prism-routed turn shows the model that actually served the
	// request, with the arrow and any savings suffix subdued.
	if a.message.PrismModelName != "" {
		routedModel := a.sty.Messages.AssistantInfoModel.Render(a.message.PrismModelName)
		arrow := a.sty.Messages.AssistantInfoProvider.Render("→")
		modelFormatted = fmt.Sprintf("%s %s %s", modelFormatted, arrow, routedModel)
	}
	if !isFinalTurn {
		return fmt.Sprintf("%s %s", icon, modelFormatted)
	}
	providerName := a.message.Provider
	if providerConfig, ok := a.cfg.Providers.Get(a.message.Provider); ok {
		providerName = providerConfig.Name
	}
	provider := a.sty.Messages.AssistantInfoProvider.Render(fmt.Sprintf("via %s", providerName))
	duration := time.Unix(finishData.Time, 0).Sub(a.lastUserMessageTime)
	infoMsg := a.sty.Messages.AssistantInfoDuration.Render(fmt.Sprintf("in %s", duration))
	assistant := fmt.Sprintf("%s %s %s %s", icon, modelFormatted, provider, infoMsg)
	return common.Section(a.sty, assistant, width)
}

// HandleKeyEvent implements KeyEventHandler. The footer's whole content
// is the line it renders, so copy hands back that line unstyled rather
// than leaving the item silently uncopyable.
func (a *AssistantInfoItem) HandleKeyEvent(msg tea.KeyMsg, keys ItemKeymap) (bool, tea.Cmd) {
	if keys.MatchesCopy(msg) {
		return true, common.CopyToClipboard(copyPlainRender(a, maxTextWidth), copyToastMessage)
	}
	return false, nil
}

// cappedMessageWidth returns the maximum width for message content for readability.
func cappedMessageWidth(availableWidth int) int {
	return min(availableWidth-MessageLeftPaddingTotal, maxTextWidth)
}

// ExtractMessageItems extracts [MessageItem]s from a [message.Message]. It
// returns all parts of the message as [MessageItem]s.
//
// For assistant messages with tool calls, pass a toolResults map to link results.
// Use BuildToolResultMap to create this map from all messages in a session.
func ExtractMessageItems(sty *styles.Styles, msg *message.Message, toolResults map[string]message.ToolResult, env ...*ItemEnv) []MessageItem {
	switch msg.Role {
	case message.User:
		// A background sub-agent's report-back is LLM-to-LLM context:
		// it surfaces as a count on its task strip entry, never here.
		if msg.SubagentNotesOnly() {
			return nil
		}
		// A harness context note is history for the model only.
		if msg.ContextNotesOnly() {
			return nil
		}
		// Nothing to render means no item: an empty entry would sit in
		// the transcript as an invisible slot the selection could land on.
		if strings.TrimSpace(msg.Content().Text) == "" && len(msg.BinaryContent()) == 0 {
			return nil
		}
		// Reconstruct shell command items from ShellCommand parts.
		var items []MessageItem
		for _, part := range msg.Parts {
			if sc, ok := part.(message.ShellCommand); ok {
				items = append(items, NewShellItem(sty, sc.Command, sc.Output, sc.ExitCode))
			}
		}
		if len(items) > 0 {
			return items
		}
		return []MessageItem{NewUserMessageItem(sty, msg)}
	case message.Assistant:
		// A summary written by an older compaction is bookkeeping, not
		// conversation: the session resumes from it, the reader never
		// sees it. Newer compactions keep their summary on the session.
		if msg.IsSummaryMessage {
			return nil
		}
		var items []MessageItem
		if ShouldRenderAssistantMessage(msg) {
			items = append(items, NewAssistantMessageItem(sty, msg))
		}
		for _, tc := range msg.ToolCalls() {
			// Dispatches render in the background tasks strip; context
			// plumbing stays in the history the model sees but is noise
			// here. One predicate decides for transcript and export both.
			if !RendersInTranscript(tc.Name, tc.Input) {
				continue
			}
			var result *message.ToolResult
			if tr, ok := toolResults[tc.ID]; ok {
				result = &tr
			}
			item := NewToolMessageItem(
				sty,
				msg.ID,
				tc,
				result,
				msg.FinishReason() == message.FinishReasonCanceled,
				firstEnv(env),
			)
			// History items have no trustworthy start time; clear the
			// constructor timestamp so no misleading timer is shown.
			if !tc.Finished {
				if restorable, ok := item.(interface{ markRestored() }); ok {
					restorable.markRestored()
				}
			}
			items = append(items, item)
		}
		return items
	}
	return []MessageItem{}
}

// HideThinking suppresses rendering of reasoning (thinking) blocks in
// the transcript when true. Reasoning is still requested, streamed and
// stored; only the rendering is suppressed. Set once at startup from
// options.tui.show_thinking, before any message items are built.
var HideThinking bool

// ShouldRenderAssistantMessage determines whether an assistant message
// renders a transcript item at all. An item exists only when something
// of it is visible: settled text, reasoning (when shown), an error or
// cancellation footer, or the working spinner of a turn that has
// produced nothing yet. Anything else — an empty finished message, a
// tool-only turn with hidden thinking — would render zero lines; such
// an item must never enter the list, where it would sit as an
// invisible slot the selection could land on.
func ShouldRenderAssistantMessage(msg *message.Message) bool {
	thinking := strings.TrimSpace(msg.ReasoningContent().Thinking)
	if HideThinking {
		thinking = ""
	}
	return strings.TrimSpace(msg.Content().Text) != "" ||
		thinking != "" ||
		msg.IsErrorLike() ||
		msg.FinishReason() == message.FinishReasonCanceled ||
		assistantSpinnerActive(msg)
}

// BuildToolResultMap creates a map of tool call IDs to their results from a list of messages.
// Tool result messages (role == message.Tool) contain the results that should be linked
// to tool calls in assistant messages.
func BuildToolResultMap(messages []*message.Message) map[string]message.ToolResult {
	resultMap := make(map[string]message.ToolResult)
	for _, msg := range messages {
		if msg.Role == message.Tool {
			for _, result := range msg.ToolResults() {
				if result.ToolCallID != "" {
					resultMap[result.ToolCallID] = result
				}
			}
		}
	}
	return resultMap
}
