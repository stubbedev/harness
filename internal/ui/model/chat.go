package model

import (
	"image"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/displaywidth"
	"github.com/clipperhouse/uax29/v2/words"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/ui/anim"
	"github.com/stubbedev/harness/internal/ui/chat"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
)

// Constants for multi-click detection.
const (
	doubleClickThreshold = 400 * time.Millisecond // 0.4s is typical double-click threshold
	clickTolerance       = 2                      // x,y tolerance for double/tripple click
)

// DelayedClickMsg is sent after the double-click threshold to trigger a
// single-click action (like expansion) if no double-click occurred.
type DelayedClickMsg struct {
	ClickID int
	ItemIdx int
	X, Y    int
}

// scrollbarHideMsg is sent to hide the scrollbar after the timeout period.
type scrollbarHideMsg struct {
	seq int // sequence number to ignore stale messages
}

// scrollbarHideCmd returns a command that sends a scrollbarHideMsg after the timeout.
func scrollbarHideCmd(seq int) tea.Cmd {
	return tea.Tick(scrollbarHideDuration, func(_ time.Time) tea.Msg {
		return scrollbarHideMsg{seq: seq}
	})
}

// resizeSettleDuration is how long after the last resize event the chat
// waits before it starts warming the message cache it skipped mid-drag.
const resizeSettleDuration = 120 * time.Millisecond

// warmBatchSize is how many messages the chat renders into the width cache
// per warming step. Kept small so no single step blocks the UI thread for
// more than a frame or so, even on slow-to-render items.
const warmBatchSize = 25

// chatWarmMsg drives one incremental cache-warming step. The first one is
// delayed until the resize settles; the rest fire immediately, one per
// batch, so warming spreads across frames instead of blocking.
type chatWarmMsg struct {
	seq int // guards against stale timers from superseded resizes
}

// chatWarmCmd schedules the next warming step after delay (zero fires as
// soon as the runtime delivers it).
func chatWarmCmd(seq int, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		return func() tea.Msg { return chatWarmMsg{seq: seq} }
	}
	return tea.Tick(delay, func(_ time.Time) tea.Msg {
		return chatWarmMsg{seq: seq}
	})
}

// Chat represents the chat UI model that handles chat interactions and
// messages.
type Chat struct {
	com      *common.Common
	list     *list.List
	idInxMap map[string]int // Map of message IDs to their indices in the list

	// itemKeys carries the bindings chat items consult for per-item actions
	// (copy, horizontal scroll). Seeded from the defaults and replaced by
	// the model layer with its possibly user-rebound keymap.
	itemKeys chat.ItemKeymap

	// animRunning is true while the shared animation clock has a tick
	// outstanding. The clock stops itself when no visible item is spinning
	// and is re-armed by EnsureAnimating once one is. animGen identifies
	// the current clock: a tick carrying an older generation belongs to a
	// clock that was superseded and is ignored, so a delayed tick can
	// never run alongside a replacement. animArmedAt lets EnsureAnimating
	// recover if a tick is ever lost.
	animRunning bool
	animGen     uint64
	animArmedAt time.Time
	// animNow is an injectable clock for tests (nil == real time).
	animNow func() time.Time

	// animAllowed gates the shared clock. setSessionMessages clears it when
	// reloading a session whose agent is not busy so ghost spinners (an
	// assistant message that never got a Finish part) stay still; the
	// message handlers re-enable it when new work arrives.
	animAllowed bool

	// Mouse state
	mouseDown     bool
	mouseDownItem int // Item index where mouse was pressed
	mouseDownX    int // X position in item content (character offset)
	mouseDownY    int // Y position in item (line offset)
	mouseDragItem int // Current item index being dragged over
	mouseDragX    int // Current X in item content
	mouseDragY    int // Current Y in item

	// Click tracking for double/triple clicks
	lastClickTime time.Time
	lastClickX    int
	lastClickY    int
	clickCount    int

	// Pending single click action (delayed to detect double-click)
	pendingClickID int // Incremented on each click to invalidate old pending clicks

	// follow is a flag to indicate whether the view should auto-scroll to
	// bottom on new messages.
	follow bool

	// manualSelection is set while the user has moved the selection to an
	// item other than the newest one. Streamed updates must not steal such
	// a selection; it is released again once the newest item is selected,
	// at which point the selection resumes following new items.
	manualSelection bool

	// drawCache memoizes the decoded form of the last list.Render output so
	// repeat frames with byte-identical content skip the per-cell ANSI
	// reparse that uv.StyledString.Draw performs every call. See F9
	// (docs/notes/2026-05-12-chat-rendering-perf.md §4.8). Bounded to one
	// entry; invalidated implicitly by string inequality on the next Draw.
	drawCache *chatDrawCache

	// Scrollbar visibility state
	scrollbarVisible bool
	scrollbarHideSeq int    // current sequence number for hide timer
	scrollbarMode    string // "default", "always", or "never"

	// resizing suppresses the O(N) total-height scan while a resize is in
	// flight (and during the incremental warm afterward), so a drag only
	// reflows the visible items. resizeSettleSeq guards stale settle/warm
	// timers; warmNext tracks warming progress through the message list.
	resizing        bool
	resizeSettleSeq int
	warmNext        int
}

// scrollbarHideDuration is how long the scrollbar remains visible after scroll activity.
const scrollbarHideDuration = 2 * time.Second

// chatDrawCache holds the pre-decoded form of the last list.Render output.
// The cache is keyed by the rendered string and the screen's width method
// (graphemes vs wcwidth pick different decoders inside ultraviolet's
// printString, so a cached buffer is only valid for the method it was
// decoded with). The cached buffer is independent of the draw area, so
// resize / scroll changes that produce the same string still hit. We cannot
// use uv.StyledString.Lines because it bottoms out at the first iteration
// against a zero-bounds rectangle (see ultraviolet styled.go line 45 — the
// shared printString loop's `y >= bounds.Max.Y` exit applies to the
// line-building branch too). A Buffer of the rendered text's natural
// dimensions is the cheapest correct shape: StyledString.Draw runs once on
// miss to populate it, and Buffer.Draw is an O(cells) cell copy with no
// ANSI re-parse on hit.
type chatDrawCache struct {
	rendered string
	method   ansi.Method
	buf      uv.ScreenBuffer
}

// NewChat creates a new instance of [Chat] that handles chat interactions and
// messages.
func NewChat(com *common.Common, scrollbarMode string) *Chat {
	c := &Chat{
		com:           com,
		idInxMap:      make(map[string]int),
		scrollbarMode: scrollbarMode,
		animAllowed:   true,
		itemKeys:      chat.DefaultItemKeymap(),
	}
	l := list.NewList()
	l.SetGap(1)
	l.RegisterRenderCallback(c.applyHighlightRange)
	l.RegisterRenderCallback(list.FocusedRenderCallback(l))
	c.list = l
	c.mouseDownItem = -1
	c.mouseDragItem = -1
	return c
}

// Height returns the height of the chat view port.
func (m *Chat) Height() int {
	return m.list.Height()
}

// Draw renders the chat UI component to the screen and the given area.
//
// The list's rendered output is cached in decoded form (see chatDrawCache) so
// that frames with byte-identical content skip the ANSI reparse that
// uv.StyledString.Draw performs on every call. The cache is keyed by the
// rendered string and the screen's width method; area / scroll changes do not
// invalidate it.
func (m *Chat) Draw(scr uv.Screen, area uv.Rectangle) {
	// Determine scrollbar visibility. Skip it entirely while resizing: the
	// thumb needs the exact total height (O(N) after a width change), which
	// is the dominant resize cost. It returns once the resize settles and
	// the cache has been warmed. The needs-scrollbar test itself uses the
	// cheap bounded overflow check.
	listHeight := m.list.Height() - 1
	needsScrollbar := false
	if !m.resizing {
		needsScrollbar = m.list.Overflows(m.list.Height())
	}

	// Determine visibility based on scrollbar mode.
	showScrollbar := false
	switch m.scrollbarMode {
	case config.ScrollbarAlways:
		showScrollbar = needsScrollbar
	case config.ScrollbarDefault:
		showScrollbar = needsScrollbar && m.scrollbarVisible
	case config.ScrollbarNever:
		showScrollbar = false
	}

	// Reserve space for scrollbar only when visible.
	scrollbarWidth := 0
	if showScrollbar {
		scrollbarWidth = 1
	}

	// Adjust list width to reserve space for scrollbar.
	listArea := area
	if scrollbarWidth > 0 {
		listArea.Max.X -= scrollbarWidth
	}

	rendered := m.list.Render()
	// While following, re-anchor to the bottom whenever the current
	// offset is not the bottom anchor. AtBottom() alone is not enough:
	// when content shrinks (a streaming item rewraps shorter, a
	// placeholder item is removed) the stale offset can still report
	// "at bottom" while the newest content floats above a blank region —
	// or, worse, offsetLine points past the end of a shrunk item and the
	// render comes out empty. Nothing corrects that until the next
	// append; re-anchoring every followed frame closes the window.
	if m.follow {
		beforeIdx, beforeLine := m.list.ScrollPosition()
		m.list.ScrollToBottom()
		afterIdx, afterLine := m.list.ScrollPosition()
		if afterIdx != beforeIdx || afterLine != beforeLine {
			rendered = m.list.Render()
		}
	}
	method, ok := scr.WidthMethod().(ansi.Method)
	if !ok {
		// Width method isn't an ansi.Method (unlikely in practice — both
		// TerminalScreen and ScreenBuffer store ansi.Method). Fall back
		// to the uncached path so behavior matches upstream exactly.
		uv.NewStyledString(rendered).Draw(scr, listArea)
	} else {
		if m.drawCache == nil ||
			m.drawCache.rendered != rendered ||
			m.drawCache.method != method {
			m.drawCache = newChatDrawCache(rendered, method)
		}
		drawCachedBuffer(scr, listArea, m.drawCache.buf)
	}

	// Draw scrollbar if visible and needed. Only reached when not resizing
	// (showScrollbar requires it), so TotalHeight is already computed and
	// cached above.
	if scrollbarWidth > 0 {
		scrollbar := common.Scrollbar(m.com.Styles, listHeight, m.list.TotalHeight()-1, listHeight, m.list.Offset())
		if scrollbar != "" {
			scrollbarArea := image.Rectangle{
				Min: image.Point{X: area.Max.X - scrollbarWidth, Y: area.Min.Y},
				Max: image.Point{X: area.Max.X, Y: area.Max.Y},
			}
			uv.NewStyledString(scrollbar).Draw(scr, scrollbarArea)
		}
	}
}

// newChatDrawCache builds a chatDrawCache for the given rendered string by
// running uv.StyledString.Draw into a fresh buffer sized to the text's
// natural bounds under the active width method. This is the only place
// ANSI decoding happens for cached frames — subsequent draws reuse buf
// via drawCachedBuffer.
//
// We can't use uv.StyledString.Bounds() here: it is hard-coded to
// ansi.GraphemeWidth, while StyledString.Draw lays cells using the
// destination buffer's WidthMethod (which we capture in `method`). For
// strings where graphemes and wcwidth disagree (emoji ZWJ sequences,
// some CJK, certain combining marks) the two answers diverge, leaving
// the cached buffer either too small (trailing cells dropped on hit) or
// too large (dead cells past the live content). Computing dimensions
// with `method.StringWidth` per line matches what printString tallies
// cell-by-cell, since both decode ANSI sequences and use the same width
// method.
func newChatDrawCache(rendered string, method ansi.Method) *chatDrawCache {
	w, h := renderedBounds(rendered, method)
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	buf := uv.NewScreenBuffer(w, h)
	buf.Method = method
	uv.NewStyledString(rendered).Draw(buf, buf.Bounds())
	return &chatDrawCache{
		rendered: rendered,
		method:   method,
		buf:      buf,
	}
}

// renderedBounds returns the (width, height) cell extent of rendered
// when laid out by method. Width is the widest line's StringWidth (which
// strips ANSI sequences and tallies cells via method, exactly like
// printString); height is the line count. Both match what
// uv.StyledString.Draw will write into a buffer whose WidthMethod is
// method, so the cache buffer is always sized to fit the live content.
func renderedBounds(rendered string, method ansi.Method) (w, h int) {
	for line := range strings.SplitSeq(rendered, "\n") {
		w = max(w, method.StringWidth(line))
		h++
	}
	return w, h
}

// drawCachedBuffer blits a previously-decoded buffer into scr at area,
// mirroring uv.StyledString.Draw's screen-mode behavior for the default
// Wrap=false, Tail="" case. The clear loop matches StyledString.Draw line
// 51-56; the buf.Draw call replaces the per-cell ANSI decode that
// printString does on every uncached frame with a pure cell copy.
func drawCachedBuffer(scr uv.Screen, area uv.Rectangle, buf uv.ScreenBuffer) {
	// Clear the area first to match StyledString.Draw — leftover cells
	// from a previous frame outside the new content must be zeroed,
	// because Buffer.Draw skips empty cells (it doesn't clear).
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			scr.SetCell(x, y, nil)
		}
	}
	buf.Draw(scr, area)
}

// BeginResize marks the chat as actively resizing so the next draws skip
// the full-height scan (and the scrollbar), reflowing only the visible
// items. It returns a command that, once resizing settles, starts warming
// the cache so the scrollbar can recompute without blocking.
func (m *Chat) BeginResize() tea.Cmd {
	m.resizing = true
	m.resizeSettleSeq++
	m.warmNext = 0
	return chatWarmCmd(m.resizeSettleSeq, resizeSettleDuration)
}

// WarmStep renders the next batch of messages into the width cache and
// returns a command to continue warming plus whether warming finished. On
// completion the resize suppression is cleared so the next draw recomputes
// the (now instant) total height and scrollbar. A stale seq — from a resize
// that has since been superseded — is a no-op returning (nil, false).
func (m *Chat) WarmStep(seq int) (cmd tea.Cmd, done bool) {
	if seq != m.resizeSettleSeq {
		return nil, false
	}
	m.warmNext = m.list.Prewarm(m.warmNext, warmBatchSize)
	if m.warmNext >= m.list.Len() {
		m.resizing = false
		return nil, true
	}
	return chatWarmCmd(seq, 0), false
}

// SetSize sets the size of the chat view port.
func (m *Chat) SetSize(width, height int) {
	// Reserve a column for the scrollbar when content overflows, decided
	// with a cheap bounded overflow check rather than the O(N) total height.
	// The final width is applied in a single SetSize so that an unchanged
	// width is a no-op — critical after warming, where re-setting the same
	// width would otherwise drop the freshly warmed cache and reintroduce
	// the blocking full render.
	// Capture whether we should stay pinned to the bottom *before* the size
	// change. A width change rewraps every item, so the list's line offsets
	// (offsetIdx/offsetLine) become stale and AtBottom() can no longer be
	// trusted afterward. follow short-circuits the AtBottom() walk in the
	// common streaming case.
	wasFollowing := m.follow || m.AtBottom()
	listWidth := width
	if m.list.Overflows(height) {
		listWidth = max(0, width-1)
	}
	m.list.SetSize(listWidth, height)
	// Re-anchor to bottom if we were pinned there before the resize.
	if wasFollowing {
		m.ScrollToBottom()
	}
}

// Len returns the number of items in the chat list.
func (m *Chat) Len() int {
	return m.list.Len()
}

// InvalidateRenderCaches drops cached rendered output on every message
// item so the next draw re-renders with the current styles.
func (m *Chat) InvalidateRenderCaches() {
	items := make([]chat.MessageItem, 0, m.list.Len())
	for i := range m.list.Len() {
		if item, ok := m.list.ItemAt(i).(chat.MessageItem); ok {
			items = append(items, item)
		}
	}
	chat.ClearItemCaches(items)
}

// SetMessages sets the chat messages to the provided list of message items.
// Consecutive tool calls are folded into collapsed groups.
func (m *Chat) SetMessages(msgs ...chat.MessageItem) tea.Cmd {
	m.scrollbarVisible = false // Reset scrollbar visibility on new session load

	items := m.foldToolGroups(msgs)
	m.list.SetItems(items...)
	m.rebuildIndices()
	m.manualSelection = false
	m.ScrollToBottom()
	return nil
}

// AppendMessages appends items in order, folding each tool call into
// the trailing open group so a run of calls between two text messages
// collapses into one row. Assistant info footers do not close a group:
// the turn they belong to may keep emitting tool calls. Items must be
// processed sequentially — absorbing a batch's tools before appending
// its text would merge them into the previous run and render the text
// after its own tool calls.
func (m *Chat) AppendMessages(msgs ...chat.MessageItem) {
	for _, msg := range msgs {
		if tool, ok := msg.(chat.ToolMessageItem); ok {
			m.absorbTool(tool)
			continue
		}
		m.list.AppendItems(msg)
	}
	m.sweepSpinnersToEnd()
	m.rebuildIndices()
}

// absorbTool folds a tool call into the trailing open group, skipping
// back over assistant info footers and the turn's working spinner, or
// starts a new group when the run was closed by a user or assistant
// text item.
//
// The spinner is the empty assistant item that stands in for the
// message still being generated. Treating it as a run boundary would
// split one run of calls into two groups and strand the animation in
// the middle of the transcript, so it is skipped here and swept back
// to the end of the list: while the turn is running it belongs below
// everything the turn has produced so far.
func (m *Chat) absorbTool(tool chat.ToolMessageItem) {
	for idx := m.list.Len() - 1; idx >= 0; idx-- {
		item := m.list.ItemAt(idx)
		if _, ok := item.(*chat.AssistantInfoItem); ok {
			continue
		}
		if chat.IsWorkingSpinner(item) {
			continue
		}
		if group, ok := item.(*chat.ToolGroupMessageItem); ok {
			group.AddTool(tool)
			m.sweepSpinnersToEnd()
			return
		}
		break
	}
	m.list.AppendItems(chat.NewToolGroupMessageItem(m.com.Styles, tool))
	m.sweepSpinnersToEnd()
}

// sweepSpinnersToEnd moves every working-spinner item to the end of
// the list, keeping their relative order. Mid-turn appends — footers,
// assistant text, tool groups — land after the spinner otherwise, and
// the thinking indicator must anchor below all of them until the turn
// settles. Indices are gathered newest-first, so removing one never
// shifts the indices still to come. The list selection never sits on a
// spinner (isSelectable refuses it), so the removals cannot drop it.
func (m *Chat) sweepSpinnersToEnd() {
	var spinners []int
	for idx := m.list.Len() - 1; idx >= 0; idx-- {
		if chat.IsWorkingSpinner(m.list.ItemAt(idx)) {
			spinners = append(spinners, idx)
		}
	}
	// Already the tail of the list: nothing to move.
	if len(spinners) == 0 || spinners[len(spinners)-1] == m.list.Len()-len(spinners) {
		return
	}
	items := make([]list.Item, 0, len(spinners))
	for _, idx := range spinners {
		items = append(items, m.list.ItemAt(idx))
		m.list.RemoveItem(idx)
	}
	slices.Reverse(items)
	m.list.AppendItems(items...)
}

// foldToolGroups folds runs of tool calls into group items. Info footers
// do not close a run and render after the group they trailed; anything
// the user reads closes it.
func (m *Chat) foldToolGroups(msgs []chat.MessageItem) []list.Item {
	out := make([]list.Item, 0, len(msgs))
	var group *chat.ToolGroupMessageItem
	var pending []list.Item
	closeRun := func() {
		if group != nil {
			out = append(out, group)
			group = nil
		}
		out = append(out, pending...)
		pending = nil
	}
	for _, msg := range msgs {
		if tool, ok := msg.(chat.ToolMessageItem); ok {
			if group == nil {
				group = chat.NewToolGroupMessageItem(m.com.Styles, tool)
			} else {
				group.AddTool(tool)
			}
			continue
		}
		if _, ok := msg.(*chat.AssistantInfoItem); ok && group != nil {
			// Hold the footer back until the run closes so it renders
			// after the group.
			pending = append(pending, msg)
			continue
		}
		if chat.IsWorkingSpinner(msg) && group != nil {
			// Same for the turn's working spinner: it is not a run
			// boundary, and it belongs below the calls it is waiting on.
			pending = append(pending, msg)
			continue
		}
		closeRun()
		out = append(out, msg)
	}
	closeRun()
	return out
}

// rebuildIndices rebuilds the ID-to-index map, registering tool group
// child IDs against their group's index.
func (m *Chat) rebuildIndices() {
	m.idInxMap = make(map[string]int, len(m.idInxMap))
	for i := range m.list.Len() {
		item, ok := m.list.ItemAt(i).(chat.MessageItem)
		if !ok {
			continue
		}
		m.idInxMap[item.ID()] = i
		if group, ok := item.(chat.ToolGroupContainer); ok {
			for _, child := range group.ToolChildren() {
				m.idInxMap[child.ID()] = i
			}
		}
	}
}

// ToolItem resolves a tool call item by ID, looking through tool groups.
func (m *Chat) ToolItem(id string) chat.ToolMessageItem {
	idx, ok := m.idInxMap[id]
	if !ok {
		return nil
	}
	item, ok := m.list.ItemAt(idx).(chat.MessageItem)
	if !ok {
		return nil
	}
	if tool, ok := item.(chat.ToolMessageItem); ok {
		return tool
	}
	if group, ok := item.(chat.ToolGroupContainer); ok {
		return group.ChildTool(id)
	}
	return nil
}

// UpdateToolItem resolves a tool call by ID, applies fn, and drops the
// containing item from the list cache so the mutation is rendered: a
// child's own version bump is invisible to the list, which only keys on
// the group's version.
func (m *Chat) UpdateToolItem(id string, fn func(chat.ToolMessageItem)) {
	tool := m.ToolItem(id)
	if tool == nil {
		return
	}
	fn(tool)
	m.InvalidateToolItem(id)
}

// InvalidateToolItem drops the list cache entry holding the given tool
// call and bumps its version so both the list cache and the frame cache
// re-render it: a child's own version bump is invisible to both.
func (m *Chat) InvalidateToolItem(id string) {
	if idx, ok := m.idInxMap[id]; ok {
		item := m.list.ItemAt(idx)
		if v, ok := item.(interface{ Bump() }); ok {
			v.Bump()
		}
		m.list.Invalidate(item)
	}
}

// SetLiveDiagnostics pushes the language servers' current per-file counts
// into every diagnostics item in the transcript. A diagnostics report is a
// point in time; without this push a file the agent has since fixed keeps
// showing its resolved errors as the last word. Groups are invalidated as
// containers: a child's own version bump is invisible to the list cache.
func (m *Chat) SetLiveDiagnostics(live map[string]lsp.DiagnosticCounts) {
	for i := range m.list.Len() {
		item := m.list.ItemAt(i)
		var setters []chat.LiveDiagnosticsSetter
		if container, ok := item.(chat.ToolGroupContainer); ok {
			for _, child := range container.ToolChildren() {
				if setter, ok := child.(chat.LiveDiagnosticsSetter); ok {
					setters = append(setters, setter)
				}
			}
		} else if setter, ok := item.(chat.LiveDiagnosticsSetter); ok {
			setters = append(setters, setter)
		}
		if len(setters) == 0 {
			continue
		}
		for _, setter := range setters {
			setter.SetLiveDiagnostics(live)
		}
		if v, ok := item.(interface{ Bump() }); ok {
			v.Bump()
		}
		m.list.Invalidate(item)
	}
}

// animTickMsg is the shared animation clock. One tick advances every
// visible spinner by a frame; there is one live clock at a time regardless
// of how many items are animating. gen is the clock generation the tick
// was armed for.
type animTickMsg struct{ gen uint64 }

// hasVisibleAnimation reports whether any item in the viewport is spinning.
func (m *Chat) hasVisibleAnimation() bool {
	if m.list.Len() == 0 {
		return false
	}
	startIdx, endIdx := m.list.VisibleItemIndices()
	for idx := startIdx; idx <= endIdx; idx++ {
		if animatable, ok := m.list.ItemAt(idx).(chat.Animatable); ok && animatable.Spinning() {
			return true
		}
	}
	return false
}

// animClockLostAfter is how long an armed clock may go without its tick
// arriving before EnsureAnimating assumes the command was lost and arms a
// new one. Generous enough that a slow frame never trips it.
const animClockLostAfter = 2 * time.Second

// SetAnimationsAllowed gates the shared animation clock. It is cleared
// when a session is reloaded whose agent is not busy so ghost spinners (an
// assistant message that never got a Finish part) stay still, and re-enabled
// whenever new messages arrive for the current session.
func (m *Chat) SetAnimationsAllowed(allowed bool) {
	m.animAllowed = allowed
}

// EnsureAnimating starts the shared animation clock if a visible item is
// spinning and no tick is outstanding. Update calls it in its tail on
// every message so any change that puts a spinner on screen (new message,
// tool update, scroll, session load) starts the clock without per-call-site
// wiring. It is the only place a tick is armed apart from Tick itself.
func (m *Chat) EnsureAnimating() tea.Cmd {
	if !m.animAllowed {
		m.animRunning = false
		return nil
	}
	if m.animRunning && m.now().Sub(m.animArmedAt) < animClockLostAfter {
		return nil
	}
	if !m.hasVisibleAnimation() {
		m.animRunning = false
		return nil
	}
	return m.armAnimClock()
}

func (m *Chat) armAnimClock() tea.Cmd {
	m.animRunning = true
	m.animArmedAt = m.now()
	m.animGen++
	gen := m.animGen
	return tea.Tick(anim.FrameInterval(), func(time.Time) tea.Msg {
		return animTickMsg{gen: gen}
	})
}

func (m *Chat) now() time.Time {
	if m.animNow != nil {
		return m.animNow()
	}
	return time.Now()
}

// stopAnimating marks the clock as stopped so the next EnsureAnimating
// re-arms it. Called while consuming the current clock's tick; bumping
// the generation also retires that clock in case the tick was not the
// current one.
func (m *Chat) stopAnimating(msg animTickMsg) {
	if msg.gen != m.animGen {
		return
	}
	m.animRunning = false
}

// Tick advances every visible spinning item by one frame. It reports
// whether any rendered output changed and returns the next tick while a
// visible item is still spinning; when none is, the clock stops and no
// command is returned. Ticks from a superseded clock generation are
// ignored so they cannot re-arm a second clock.
func (m *Chat) Tick(msg animTickMsg) (changed bool, cmd tea.Cmd) {
	if msg.gen != m.animGen {
		return false, nil
	}
	m.animRunning = false
	if m.list.Len() == 0 {
		return false, nil
	}
	spinning := false
	startIdx, endIdx := m.list.VisibleItemIndices()
	for idx := startIdx; idx <= endIdx; idx++ {
		animatable, ok := m.list.ItemAt(idx).(chat.Animatable)
		if !ok || !animatable.Spinning() {
			continue
		}
		spinning = true
		if animatable.Advance() {
			changed = true
		}
	}
	if !spinning {
		return changed, nil
	}
	return changed, m.armAnimClock()
}

// Focus sets the focus state of the chat component.
func (m *Chat) Focus() {
	m.list.Focus()
}

// Blur removes the focus state from the chat component.
func (m *Chat) Blur() {
	m.list.Blur()
}

// FocusRestoringSelection focuses the transcript and returns a command
// scrolling the selection into view. When the user last moved the
// selection off the newest item and that item is still in view, focus
// returns to it; otherwise it lands on the newest one, leaving the
// viewport wherever the conversation has since scrolled to.
func (m *Chat) FocusRestoringSelection() tea.Cmd {
	m.Focus()
	if m.HasManualSelection() && m.SelectedItemInView() {
		return m.ScrollToSelected()
	}
	m.SetSelected(m.Len() - 1)
	return nil
}

// FocusSelectingNewest focuses the transcript the way an upward key
// gesture enters it from below: the selection always lands on the
// newest entry, scrolled into view when the conversation has scrolled
// past it, with the same from-below landing SelectPrev applies.
func (m *Chat) FocusSelectingNewest() tea.Cmd {
	m.Focus()
	m.SelectLast()
	m.landSelectionFromBelow()
	return m.ScrollToSelected()
}

// ScrollPosition returns the list's first visible item index and the line
// offset into it.
func (m *Chat) ScrollPosition() (offsetIdx, offsetLine int) {
	return m.list.ScrollPosition()
}

// Offset returns the scroll offset in lines from the top of the list.
func (m *Chat) Offset() int {
	return m.list.Offset()
}

// Selected returns the index of the selected item.
func (m *Chat) Selected() int {
	return m.list.Selected()
}

// Focused returns whether the chat list is focused.
func (m *Chat) Focused() bool {
	return m.list.Focused()
}

// RenderState captures everything about the chat that affects its rendered
// output and is not carried by item versions. Anything added to Chat that
// Draw reads belongs here, so callers that memoize whole frames stay correct
// without knowing Chat's internals.
type RenderState struct {
	OffsetIdx        int
	OffsetLine       int
	Selected         int
	Focused          bool
	ScrollbarVisible bool
	// ItemsVersion changes when any message mutates its rendered output.
	ItemsVersion uint64
}

// RenderState returns the current render-affecting chat state. It renders
// nothing; the only non-constant part is the item version fold, which reads
// one field per message.
func (m *Chat) RenderState() RenderState {
	offsetIdx, offsetLine := m.ScrollPosition()
	return RenderState{
		OffsetIdx:        offsetIdx,
		OffsetLine:       offsetLine,
		Selected:         m.Selected(),
		Focused:          m.Focused(),
		ScrollbarVisible: m.scrollbarVisible,
		ItemsVersion:     m.list.ItemsVersion(),
	}
}

// AtBottom returns whether the chat list is currently scrolled to the bottom.
func (m *Chat) AtBottom() bool {
	return m.list.AtBottom()
}

// Follow returns whether the chat view is in follow mode (auto-scroll to
// bottom on new messages).
func (m *Chat) Follow() bool {
	return m.follow
}

// ScrollToBottom scrolls the chat view to the bottom.
// Does not trigger scrollbar visibility (auto-scroll to show new content).
func (m *Chat) ScrollToBottom() tea.Cmd {
	m.list.ScrollToBottom()
	m.follow = true
	return nil
}

// ScrollToTop scrolls the chat view to the top.
func (m *Chat) ScrollToTop() tea.Cmd {
	m.list.ScrollToTop()
	m.follow = false // Disable follow mode when user scrolls up
	return m.showScrollbar()
}

// ScrollBy scrolls the chat view by the given number of line deltas.
func (m *Chat) ScrollBy(lines int) tea.Cmd {
	m.list.ScrollBy(lines)
	if lines < 0 {
		// Scrolling up always disables follow mode.
		m.follow = false
	} else if m.AtBottom() {
		// Scrolling down re-enables follow when we reach the bottom.
		m.follow = true
	}
	return m.showScrollbar()
}

// ScrollToSelected scrolls the chat view to the selected item — to
// the sub-selection's lines when the selection sits inside an expanded
// tool group, which may be far inside a run taller than the viewport.
func (m *Chat) ScrollToSelected() tea.Cmd {
	m.list.ScrollToSelected()
	m.follow = m.AtBottom() // Disable follow mode if user scrolls up
	return m.showScrollbar()
}

// ScrollToIndex scrolls the chat view to the item at the given index.
func (m *Chat) ScrollToIndex(index int) tea.Cmd {
	m.list.ScrollToIndex(index)
	m.follow = m.AtBottom() // Disable follow mode if user scrolls up
	return m.showScrollbar()
}

// ScrollItemIntoView scrolls the chat view the minimum distance needed to
// make the item at the given index fully visible, without disturbing the
// viewport when the item already fits.
func (m *Chat) ScrollItemIntoView(index int) tea.Cmd {
	m.list.ScrollItemIntoView(index)
	m.follow = m.AtBottom()
	return m.showScrollbar()
}

// reanchorAfterExpand restores the view after an item's rendered height
// changed: while following, stay pinned to the bottom; otherwise scroll
// just enough to bring the changed item back into view. An item taller
// than the viewport top-aligns so the expansion is readable from its
// start.
func (m *Chat) reanchorAfterExpand() {
	if m.follow {
		m.ScrollToBottom()
		return
	}
	m.ScrollItemIntoView(m.list.Selected())
}

// showScrollbar makes the scrollbar visible and returns a command to hide it after timeout.
func (m *Chat) showScrollbar() tea.Cmd {
	// Only start timer for "default" mode
	if m.scrollbarMode != config.ScrollbarDefault {
		return nil
	}
	m.scrollbarVisible = true
	m.scrollbarHideSeq++
	return scrollbarHideCmd(m.scrollbarHideSeq)
}

// HideScrollbar hides the scrollbar if the sequence matches.
func (m *Chat) HideScrollbar(seq int) {
	// Only hide scrollbar for "default" mode
	if m.scrollbarMode != config.ScrollbarDefault {
		return
	}
	if seq == m.scrollbarHideSeq {
		m.scrollbarVisible = false
	}
}

// ScrollToBottomAndSelectLast scrolls the chat view to the bottom, selects
// the last item, and returns a command to restart any paused animations that
// are now visible.
func (m *Chat) ScrollToBottomAndSelectLast() tea.Cmd {
	m.ScrollToBottom()
	m.SelectLast()
	return nil
}

// SelectedItemInView returns whether the selected item is currently in view.
func (m *Chat) SelectedItemInView() bool {
	return m.list.SelectedItemInView()
}

func (m *Chat) isSelectable(index int) bool {
	item := m.list.ItemAt(index)
	if item == nil {
		return false
	}
	// Transient items — the working spinner, the live thinking entry —
	// keep changing underneath the reader and hold nothing stable to
	// select or copy.
	if chat.IsTransient(item) {
		return false
	}
	_, ok := item.(list.Focusable)
	return ok
}

// HasManualSelection reports whether the user has moved the selection to
// an item other than the newest, so new items must leave it alone.
func (m *Chat) HasManualSelection() bool {
	return m.manualSelection
}

// refreshManualSelection records whether the selection currently sits on
// an item other than the newest selectable one. Every selection change
// runs through it, so selecting the newest item again releases the manual
// hold and the selection follows new items once more.
func (m *Chat) refreshManualSelection() {
	sel := m.list.Selected()
	m.manualSelection = sel >= 0 && sel != m.lastSelectableIndex()
}

// lastSelectableIndex returns the index of the newest selectable item,
// or -1 when the list has no selectable item.
func (m *Chat) lastSelectableIndex() int {
	for i := m.list.Len() - 1; i >= 0; i-- {
		if m.isSelectable(i) {
			return i
		}
	}
	return -1
}

// selectFrom moves the selection to the nearest selectable item in the
// direction step walks, starting at start's neighbor. The selection is
// written only when such an item is found, so a walk that runs off the
// end leaves it exactly where it was.
func (m *Chat) selectFrom(start int, step func(int) int) bool {
	for idx := step(start); idx >= 0; idx = step(idx) {
		if m.isSelectable(idx) {
			m.list.SetSelected(idx)
			return true
		}
	}
	return false
}

// SetSelected sets the selected message to index when it is selectable,
// and otherwise to the nearest selectable item below it, falling back
// to the nearest one above.
func (m *Chat) SetSelected(index int) {
	defer m.refreshManualSelection()
	if m.isSelectable(index) {
		m.list.SetSelected(index)
		return
	}
	if m.selectFrom(index, m.list.IndexAfter) {
		return
	}
	m.selectFrom(index, m.list.IndexBefore)
}

// landSelectionFromBelow places the selected item's internal cursor
// for a keyboard arrival from the item below: an expanded tool group
// parks the sub-cursor on its bottommost child, so the next up walks
// the run's calls instead of skipping them. Every other item is a
// single row and keeps no cursor of its own.
func (m *Chat) landSelectionFromBelow() {
	if g, ok := m.selectedGroup(); ok {
		g.SelectChildFromBelow()
	}
}

// SelectPrev selects the previous selectable message in the chat list.
// It reports whether one exists above the current selection; when none
// does, the selection is left untouched. Arriving from below, an
// expanded tool group parks the sub-cursor on its bottommost child.
func (m *Chat) SelectPrev() bool {
	defer m.refreshManualSelection()
	if !m.selectFrom(m.list.Selected(), m.list.IndexBefore) {
		return false
	}
	m.landSelectionFromBelow()
	return true
}

// SelectNext selects the next selectable message in the chat list. It
// reports whether one exists below the current selection; when none
// does, the selection is left untouched.
func (m *Chat) SelectNext() bool {
	defer m.refreshManualSelection()
	return m.selectFrom(m.list.Selected(), m.list.IndexAfter)
}

// SelectFirst selects the first selectable message in the chat list.
func (m *Chat) SelectFirst() bool {
	defer m.refreshManualSelection()
	return m.selectFrom(-1, m.list.IndexAfter)
}

// SelectLast selects the last selectable message in the chat list.
func (m *Chat) SelectLast() bool {
	defer m.refreshManualSelection()
	return m.selectFrom(m.list.Len(), m.list.IndexBefore)
}

// SelectFirstInView selects the first message currently in view.
func (m *Chat) SelectFirstInView() {
	defer m.refreshManualSelection()
	startIdx, endIdx := m.list.VisibleItemIndices()
	for i := startIdx; i <= endIdx; i++ {
		if m.isSelectable(i) {
			m.list.SetSelected(i)
			return
		}
	}
}

// SelectLastInView selects the last message currently in view.
func (m *Chat) SelectLastInView() {
	defer m.refreshManualSelection()
	startIdx, endIdx := m.list.VisibleItemIndices()
	for i := endIdx; i >= startIdx; i-- {
		if m.isSelectable(i) {
			m.list.SetSelected(i)
			return
		}
	}
}

// SelectNearestInView moves an out-of-view selection to the visible edge
// nearest to it: the top row when the selection is above the viewport, the
// bottom row when it is below. With no selection, scrolledUp picks the
// bottom row (the content the user is moving towards) and otherwise the
// top row.
func (m *Chat) SelectNearestInView(scrolledUp bool) {
	startIdx, _ := m.list.VisibleItemIndices()
	sel := m.list.Selected()
	switch {
	case sel < 0:
		if scrolledUp {
			m.SelectLastInView()
		} else {
			m.SelectFirstInView()
		}
	case sel < startIdx:
		m.SelectFirstInView()
	default:
		m.SelectLastInView()
	}
}

// ClearMessages removes all messages from the chat list.
func (m *Chat) ClearMessages() {
	m.idInxMap = make(map[string]int)
	m.scrollbarVisible = false
	m.manualSelection = false
	m.list.SetItems()
	m.ClearMouse()
}

// RemoveMessage removes a message from the chat list by its ID.
func (m *Chat) RemoveMessage(id string) {
	idx, ok := m.idInxMap[id]
	if !ok {
		return
	}

	// Remove from list
	m.list.RemoveItem(idx)

	// The removed item may have been the only thing separating two tool
	// runs: every assistant message gets an (empty) text item at created
	// time, and a tool-only message drops it again once its calls arrive.
	// Merge the runs the empty separator split so consecutive calls end
	// up in one group after all.
	if idx > 0 && idx < m.list.Len() {
		prev, okPrev := m.list.ItemAt(idx - 1).(*chat.ToolGroupMessageItem)
		next, okNext := m.list.ItemAt(idx).(*chat.ToolGroupMessageItem)
		if okPrev && okNext && prev != next {
			for _, tool := range next.ToolChildren() {
				prev.AddTool(tool)
			}
			m.list.RemoveItem(idx)
		}
	}
	m.rebuildIndices()
}

// MessageItem returns the message item with the given ID, or nil if not found.
func (m *Chat) MessageItem(id string) chat.MessageItem {
	idx, ok := m.idInxMap[id]
	if !ok {
		return nil
	}
	item, ok := m.list.ItemAt(idx).(chat.MessageItem)
	if !ok {
		return nil
	}
	return item
}

// selectedGroup returns the selected item as a tool group when it is
// one — the single place that decides which chat item is a group.
func (m *Chat) selectedGroup() (*chat.ToolGroupMessageItem, bool) {
	g, ok := m.list.SelectedItem().(*chat.ToolGroupMessageItem)
	return g, ok
}

// ToggleExpandedSelectedItem expands the selected message item if it is expandable.
func (m *Chat) ToggleExpandedSelectedItem() {
	// With the sub-cursor on a child line, toggle that one call
	// between its one-liner and full view.
	if g, ok := m.selectedGroup(); ok && g.ToggleSelectedChild() {
		m.reanchorAfterExpand()
		return
	}
	if expandable, ok := m.list.SelectedItem().(chat.Expandable); ok {
		_ = expandable.ToggleExpanded()
		m.reanchorAfterExpand()
	}
}

// EnterSelectedItem implements the enter key: go in one level. On a
// tool group, enter on the group row opens the one-liner level and
// drops the sub-cursor on the first call, and enter on a call line
// opens that call's full view; anything else expands like space.
func (m *Chat) EnterSelectedItem() {
	if g, ok := m.selectedGroup(); ok {
		g.DigIn()
		m.reanchorAfterExpand()
		return
	}
	m.ToggleExpandedSelectedItem()
}

// AscendSelectedItem implements the escape key: go out one level. A
// tool group consumes the escape while it has a level to leave (a
// fully rendered call under the sub-cursor, or the open group);
// anything else falls through to the caller's escape handling. When
// the escape shrank the render, the view re-anchors on the item so the
// content the expansion pushed off-screen comes back.
func (m *Chat) AscendSelectedItem() bool {
	g, ok := m.selectedGroup()
	if !ok {
		return false
	}
	wasOpen := g.ExpandedLevel()
	wasRendered := g.FullyRenderedChildren()
	if !g.Ascend() {
		return false
	}
	if (wasOpen && !g.ExpandedLevel()) || g.FullyRenderedChildren() < wasRendered {
		m.ScrollItemIntoView(m.list.Selected())
	}
	return true
}

// subCursorMove advances the selected group's sub-cursor with step,
// keeping the sub-selection's lines on screen, and reports whether the
// sub-cursor consumed the key — in which case list selection must not
// move.
func (m *Chat) subCursorMove(step func(*chat.ToolGroupMessageItem) bool) bool {
	g, ok := m.selectedGroup()
	if !ok || !step(g) {
		return false
	}
	m.ScrollToSelected()
	return true
}

// SubCursorDown handles down-navigation into an expanded group's
// children, keeping the sub-cursor's lines on screen. It reports
// whether the sub-cursor consumed the key, in which case list
// selection must not move.
func (m *Chat) SubCursorDown() bool {
	return m.subCursorMove((*chat.ToolGroupMessageItem).SelectChildNext)
}

// SubCursorUp handles up-navigation out of a group's children, keeping
// the sub-cursor's lines on screen. It reports whether the sub-cursor
// consumed the key.
func (m *Chat) SubCursorUp() bool {
	return m.subCursorMove((*chat.ToolGroupMessageItem).SelectChildPrev)
}

// IsSelectedShellItem returns true if the currently selected item is a
// ShellItem (bang-mode result).
func (m *Chat) IsSelectedShellItem() bool {
	_, ok := m.list.SelectedItem().(*chat.ShellItem)
	return ok
}

// ScrollSelectedShellHorizontal scrolls the selected ShellItem horizontally
// by delta columns. No-op if the selected item is not a ShellItem.
func (m *Chat) ScrollSelectedShellHorizontal(delta int) {
	if shell, ok := m.list.SelectedItem().(*chat.ShellItem); ok {
		shell.ScrollHorizontal(delta)
	}
}

// SetItemKeymap replaces the bindings chat items consult for per-item
// actions, so options.tui.keybinds overrides apply there as well.
func (m *Chat) SetItemKeymap(keys chat.ItemKeymap) {
	m.itemKeys = keys
}

// HandleKeyMsg handles key events for the chat component.
func (m *Chat) HandleKeyMsg(key tea.KeyMsg) (bool, tea.Cmd) {
	if m.list.Focused() {
		if handler, ok := m.list.SelectedItem().(chat.KeyEventHandler); ok {
			return handler.HandleKeyEvent(key, m.itemKeys)
		}
	}
	return false, nil
}

// HandleMouseDown handles mouse down events for the chat component.
// It detects single, double, and triple clicks for text selection.
// Returns whether the click was handled and an optional command for delayed
// single-click actions.
func (m *Chat) HandleMouseDown(x, y int) (bool, tea.Cmd) {
	if m.list.Len() == 0 {
		return false, nil
	}

	itemIdx, itemY := m.list.ItemIndexAtPosition(x, y)
	if itemIdx < 0 {
		return false, nil
	}
	if !m.isSelectable(itemIdx) {
		return false, nil
	}

	// Increment pending click ID to invalidate any previous pending clicks.
	m.pendingClickID++
	clickID := m.pendingClickID

	// Detect multi-click (double/triple)
	now := time.Now()
	if now.Sub(m.lastClickTime) <= doubleClickThreshold &&
		abs(x-m.lastClickX) <= clickTolerance &&
		abs(y-m.lastClickY) <= clickTolerance {
		m.clickCount++
	} else {
		m.clickCount = 1
	}
	m.lastClickTime = now
	m.lastClickX = x
	m.lastClickY = y

	// Select the item that was clicked
	m.list.SetSelected(itemIdx)
	m.refreshManualSelection()

	var cmd tea.Cmd

	switch m.clickCount {
	case 1:
		// Single click - start selection and schedule delayed click action.
		m.mouseDown = true
		m.mouseDownItem = itemIdx
		m.mouseDownX = x
		m.mouseDownY = itemY
		m.mouseDragItem = itemIdx
		m.mouseDragX = x
		m.mouseDragY = itemY

		// Schedule delayed click action (e.g., expansion) after a short delay.
		// If a double-click occurs, the clickID will be invalidated.
		cmd = tea.Tick(doubleClickThreshold, func(t time.Time) tea.Msg {
			return DelayedClickMsg{
				ClickID: clickID,
				ItemIdx: itemIdx,
				X:       x,
				Y:       itemY,
			}
		})
	case 2:
		// Double click - select word (no delayed action)
		m.selectWord(itemIdx, x, itemY)
	case 3:
		// Triple click - select line (no delayed action)
		m.selectLine(itemIdx, itemY)
		m.clickCount = 0 // Reset after triple click
	}

	return true, cmd
}

// HandleDelayedClick handles a delayed single-click action (like expansion).
// It only executes if the click ID matches (i.e., no double-click occurred)
// and no text selection was made (drag to select).
func (m *Chat) HandleDelayedClick(msg DelayedClickMsg) bool {
	// Ignore if this click was superseded by a newer click (double/triple).
	if msg.ClickID != m.pendingClickID {
		return false
	}

	// Don't expand if user dragged to select text.
	if m.HasHighlight() {
		return false
	}

	// Execute the click action (e.g., expansion).
	selectedItem := m.list.SelectedItem()
	if clickable, ok := selectedItem.(list.MouseClickable); ok {
		handled := clickable.HandleMouseClick(ansi.MouseButton1, msg.X, msg.Y)
		// Toggle expansion only when the item signalled it handled the
		// click. Items like AssistantMessageItem only report handled when
		// the click is on their expandable region, so this avoids
		// toggling expansion for clicks outside the clickable area.
		if handled {
			if expandable, ok := selectedItem.(chat.Expandable); ok {
				_ = expandable.ToggleExpanded()
				m.reanchorAfterExpand()
			}
		}
		return handled
	}

	return false
}

// HandleMouseUp handles mouse up events for the chat component.
func (m *Chat) HandleMouseUp(x, y int) bool {
	if !m.mouseDown {
		return false
	}

	m.mouseDown = false
	return true
}

// HandleMouseDrag handles mouse drag events for the chat component.
func (m *Chat) HandleMouseDrag(x, y int) bool {
	if !m.mouseDown {
		return false
	}

	if m.list.Len() == 0 {
		return false
	}

	itemIdx, itemY := m.list.ItemIndexAtPosition(x, y)
	if itemIdx < 0 {
		return false
	}

	m.mouseDragItem = itemIdx
	m.mouseDragX = x
	m.mouseDragY = itemY

	return true
}

// HasHighlight returns whether there is currently highlighted content.
func (m *Chat) HasHighlight() bool {
	startItemIdx, startLine, startCol, endItemIdx, endLine, endCol := m.getHighlightRange()
	return startItemIdx >= 0 && endItemIdx >= 0 && (startLine != endLine || startCol != endCol)
}

// HighlightContent returns the currently highlighted content based on the mouse
// selection. It returns an empty string if no content is highlighted.
func (m *Chat) HighlightContent() string {
	startItemIdx, startLine, startCol, endItemIdx, endLine, endCol := m.getHighlightRange()
	if startItemIdx < 0 || endItemIdx < 0 || startLine == endLine && startCol == endCol {
		return ""
	}

	var sb strings.Builder
	for i := startItemIdx; i <= endItemIdx; i++ {
		item := m.list.ItemAt(i)
		if hi, ok := item.(list.Highlightable); ok {
			startLine, startCol, endLine, endCol := hi.Highlight()
			listWidth := m.list.Width()
			var rendered string
			if rr, ok := item.(list.RawRenderable); ok {
				rendered = rr.RawRender(listWidth)
			} else {
				rendered = item.Render(listWidth)
			}
			sb.WriteString(list.HighlightContent(
				rendered,
				uv.Rect(0, 0, listWidth, lipgloss.Height(rendered)),
				startLine,
				startCol,
				endLine,
				endCol,
			))
			sb.WriteString(strings.Repeat("\n", m.list.Gap()))
		}
	}

	return strings.TrimSpace(sb.String())
}

// ClearMouse clears the current mouse interaction state.
func (m *Chat) ClearMouse() {
	m.mouseDown = false
	m.mouseDownItem = -1
	m.mouseDragItem = -1
	m.lastClickTime = time.Time{}
	m.lastClickX = 0
	m.lastClickY = 0
	m.clickCount = 0
	m.pendingClickID++ // Invalidate any pending delayed click
}

// applyHighlightRange applies the current highlight range to the chat items.
func (m *Chat) applyHighlightRange(idx, selectedIdx int, item list.Item) list.Item {
	if hi, ok := item.(list.Highlightable); ok {
		// Apply highlight. Columns cross from viewport space, which
		// getHighlightRange speaks, into the item content space
		// SetHighlight speaks; this is the only place that bridge
		// happens.
		startItemIdx, startLine, startCol, endItemIdx, endLine, endCol := m.getHighlightRange()
		sLine, sCol, eLine, eCol := -1, -1, -1, -1
		if idx >= startItemIdx && idx <= endItemIdx {
			if idx == startItemIdx && idx == endItemIdx {
				// Single item selection
				sLine = startLine
				sCol = chat.ContentCol(startCol)
				eLine = endLine
				eCol = chat.ContentCol(endCol)
			} else if idx == startItemIdx {
				// First item - from start position to end of item
				sLine = startLine
				sCol = chat.ContentCol(startCol)
				eLine = -1
				eCol = -1
			} else if idx == endItemIdx {
				// Last item - from start of item to end position
				sLine = 0
				sCol = 0
				eLine = endLine
				eCol = chat.ContentCol(endCol)
			} else {
				// Middle item - fully highlighted
				sLine = 0
				sCol = 0
				eLine = -1
				eCol = -1
			}
		}

		hi.SetHighlight(sLine, sCol, eLine, eCol)
		return hi.(list.Item)
	}

	return item
}

// getHighlightRange returns the current highlight range. The stored
// mouse coordinates name cells, and both the pressed and the released
// cell are inside the selection, the way terminal-native selection
// behaves: the range's right edge is one past the stored end cell. A
// range that covers a single cell is normalized to empty, so a plain
// click neither paints nor copies and dragging within one cell
// selects nothing. This is the single source of selection geometry
// for painting, copying, and degeneracy checks alike.
func (m *Chat) getHighlightRange() (startItemIdx, startLine, startCol, endItemIdx, endLine, endCol int) {
	if m.mouseDownItem < 0 {
		return -1, -1, -1, -1, -1, -1
	}

	downItemIdx := m.mouseDownItem
	dragItemIdx := m.mouseDragItem

	// Determine selection direction
	draggingDown := dragItemIdx > downItemIdx ||
		(dragItemIdx == downItemIdx && m.mouseDragY > m.mouseDownY) ||
		(dragItemIdx == downItemIdx && m.mouseDragY == m.mouseDownY && m.mouseDragX >= m.mouseDownX)

	if draggingDown {
		// Normal forward selection
		startItemIdx = downItemIdx
		startLine = m.mouseDownY
		startCol = m.mouseDownX
		endItemIdx = dragItemIdx
		endLine = m.mouseDragY
		endCol = m.mouseDragX + 1
	} else {
		// Backward selection (dragging up)
		startItemIdx = dragItemIdx
		startLine = m.mouseDragY
		startCol = m.mouseDragX
		endItemIdx = downItemIdx
		endLine = m.mouseDownY
		endCol = m.mouseDownX + 1
	}

	// Collapse a single-cell range: press and release sit on the same
	// cell, so nothing is selected.
	if startItemIdx == endItemIdx && startLine == endLine && endCol-startCol <= 1 {
		return -1, -1, -1, -1, -1, -1
	}

	return startItemIdx, startLine, startCol, endItemIdx, endLine, endCol
}

// setSelectionCells records a selection spanning content columns
// [firstCol, lastCol] inclusive on one line of one item, converting
// into the viewport cells the stored mouse state and
// getHighlightRange work with. Every programmatic selection (word,
// line) stores through here, so the inclusive-cell contract has a
// single definition; a range whose two ends coincide collapses to no
// selection in getHighlightRange, which is the plain-click behavior.
func (m *Chat) setSelectionCells(itemIdx, line, firstCol, lastCol int) {
	m.mouseDown = true
	m.mouseDownItem = itemIdx
	m.mouseDownY = line
	m.mouseDownX = chat.ViewportCol(firstCol)
	m.mouseDragItem = itemIdx
	m.mouseDragY = line
	m.mouseDragX = chat.ViewportCol(lastCol)
}

// selectWord selects the word at the given position within an item.
func (m *Chat) selectWord(itemIdx, x, itemY int) {
	item := m.list.ItemAt(itemIdx)
	if item == nil {
		return
	}

	// Get the rendered content for this item
	var rendered string
	if rr, ok := item.(list.RawRenderable); ok {
		rendered = rr.RawRender(m.list.Width())
	} else {
		rendered = item.Render(m.list.Width())
	}

	lines := strings.Split(rendered, "\n")
	if itemY < 0 || itemY >= len(lines) {
		return
	}

	// The click lands in viewport space; the rendered lines are item
	// content, so convert once and stay in content space until the
	// selection is stored.
	line := ansi.Strip(lines[itemY])
	startCol, endCol := findWordBoundaries(line, chat.ContentCol(x))
	if startCol == endCol {
		// No word found at position, fallback to single click behavior
		// on the clicked cell.
		cc := chat.ContentCol(x)
		m.setSelectionCells(itemIdx, itemY, cc, cc)
		return
	}

	// endCol is exclusive; the selection covers the word's last cell.
	m.setSelectionCells(itemIdx, itemY, startCol, endCol-1)
}

// selectLine selects the entire line at the given position within an item.
func (m *Chat) selectLine(itemIdx, itemY int) {
	item := m.list.ItemAt(itemIdx)
	if item == nil {
		return
	}

	// Get the rendered content for this item
	var rendered string
	if rr, ok := item.(list.RawRenderable); ok {
		rendered = rr.RawRender(m.list.Width())
	} else {
		rendered = item.Render(m.list.Width())
	}

	lines := strings.Split(rendered, "\n")
	if itemY < 0 || itemY >= len(lines) {
		return
	}

	// Get line length (stripped of ANSI codes); the rendered lines are
	// item content, so the selection spans its first through last
	// cell.
	lineLen := ansi.StringWidth(lines[itemY])
	m.setSelectionCells(itemIdx, itemY, 0, lineLen-1)
}

// findWordBoundaries finds the start and end column of the word at the given column.
// Returns (startCol, endCol) where endCol is exclusive.
func findWordBoundaries(line string, col int) (startCol, endCol int) {
	if line == "" || col < 0 {
		return 0, 0
	}

	i := displaywidth.StringGraphemes(line)
	for i.Next() {
	}

	// Segment the line into words using UAX#29.
	lineCol := 0 // tracks the visited column widths
	lastCol := 0 // tracks the start of the current token
	iter := words.FromString(line)
	for iter.Next() {
		token := iter.Value()
		tokenWidth := displaywidth.String(token)

		graphemeStart := lineCol
		graphemeEnd := lineCol + tokenWidth
		lineCol += tokenWidth

		// If clicked before this token, return the previous token boundaries.
		if col < graphemeStart {
			return lastCol, lastCol
		}

		// Update lastCol to the end of this token for next iteration.
		lastCol = graphemeEnd

		// If clicked within this token, return its boundaries.
		if col >= graphemeStart && col < graphemeEnd {
			// If clicked on whitespace, return empty selection.
			if strings.TrimSpace(token) == "" {
				return col, col
			}
			return graphemeStart, graphemeEnd
		}
	}

	return col, col
}

// abs returns the absolute value of an integer.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
