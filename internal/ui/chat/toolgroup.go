package chat

import (
	"encoding/json"
	"fmt"
	"maps"
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

// ToolGroupMessageItem groups the consecutive tool calls of a turn into
// one collapsed row - "Running (N tool calls)" while calls are in
// flight, "Ran (N tool calls)" once the run has settled - so the
// transcript is not dominated by tool bodies nobody asked for.
// Expansion has two levels:
// the first shows one line per call (status glyph, tool name, argument
// summary), the second renders the calls' full output. A singleton group
// renders as the bare one-liner; expanding it shows the full render.
//
// Children are never list items of their own: their IDs are aliased to
// the group's index in the chat idInxMap and Chat.ToolItem resolves
// through the group.
type ToolGroupMessageItem struct {
	*list.Versioned
	*cachedMessageItem
	*focusableMessageItem

	sty      *styles.Styles
	id       string
	tools    []ToolMessageItem
	expanded bool
	// selectedChild is the keyboard sub-cursor: -1 sits on the group
	// row itself, >=0 on that child's line. It only shows while the
	// group is focused, and is cleared when the selection moves off the
	// group - not when focus merely leaves the list, so it is still
	// there when focus comes back.
	selectedChild int
}

var (
	_ MessageItem         = (*ToolGroupMessageItem)(nil)
	_ Expandable          = (*ToolGroupMessageItem)(nil)
	_ Animatable          = (*ToolGroupMessageItem)(nil)
	_ KeyEventHandler     = (*ToolGroupMessageItem)(nil)
	_ list.MouseClickable = (*ToolGroupMessageItem)(nil)
	_ ToolGroupContainer  = (*ToolGroupMessageItem)(nil)
	_ list.SubSelectable  = (*ToolGroupMessageItem)(nil)
	_ list.SelectionAware = (*ToolGroupMessageItem)(nil)
)

// ToolGroupContainer is implemented by tool group items so the chat can
// resolve child tool calls through the group.
type ToolGroupContainer interface {
	ChildTool(id string) ToolMessageItem
	ToolChildren() []ToolMessageItem
}

// advanceNested advances every spinning animatable tool in tools and
// reports whether any of them changed. Moved here from the retired
// agent-item renderer; the group drives its children the same way.
func advanceNested(tools []ToolMessageItem) bool {
	changed := false
	for _, nestedTool := range tools {
		if s, ok := nestedTool.(Animatable); ok && s.Spinning() && s.Advance() {
			changed = true
		}
	}
	return changed
}

// NewToolGroupMessageItem creates a group holding the given tool items.
func NewToolGroupMessageItem(sty *styles.Styles, first ToolMessageItem) *ToolGroupMessageItem {
	v := list.NewVersioned()
	g := &ToolGroupMessageItem{
		Versioned:            v,
		cachedMessageItem:    newCachedMessageItem(v),
		focusableMessageItem: newFocusableMessageItem(v),
		sty:                  sty,
		id:                   "toolgroup-" + first.ID(),
		tools:                []ToolMessageItem{first},
		// The sub-cursor starts on the group row. Without this the
		// zero value reads as "on the first child", which the render
		// path only got away with because it also checks focus.
		selectedChild: -1,
	}
	return g
}

// ID implements [Identifiable].
func (g *ToolGroupMessageItem) ID() string { return g.id }

// SetSelected implements [list.SelectionAware], clearing the sub-cursor
// when the selection moves off the group, so a later arrival starts on
// the group row.
func (g *ToolGroupMessageItem) SetSelected(selected bool) {
	if !selected && g.selectedChild != -1 {
		g.selectedChild = -1
		g.clearCache()
	}
}

// ExpandedLevel reports whether the group shows its one-liner level.
func (g *ToolGroupMessageItem) ExpandedLevel() bool { return g.expanded }

// showsChildRows reports whether the group renders one row per call:
// the one-liner level open on a multi-call run. A singleton never
// does; its only line is the call itself.
func (g *ToolGroupMessageItem) showsChildRows() bool {
	return g.expanded && len(g.tools) > 1
}

// FullyRenderedChildren counts the calls currently showing their full
// view, so callers can tell whether a level change shrank the render.
func (g *ToolGroupMessageItem) FullyRenderedChildren() int {
	n := 0
	for _, t := range g.tools {
		if ShowsFullView(t) {
			n++
		}
	}
	return n
}

// SelectedChild returns the sub-cursor's child index, or -1 when the
// cursor is on the group row.
func (g *ToolGroupMessageItem) SelectedChild() int { return g.selectedChild }

// SelectChildNext advances the sub-cursor one line down, descending
// from the group row into the children. Reports whether the cursor
// moved (the caller otherwise falls through to list navigation).
func (g *ToolGroupMessageItem) SelectChildNext() bool {
	if !g.expanded || g.selectedChild >= len(g.tools)-1 {
		return false
	}
	g.selectedChild++
	g.invalidate()
	return true
}

// SelectChildPrev moves the sub-cursor one line up. At the first child
// it parks the cursor back on the group row rather than leaving the
// group; a second up then leaves via list navigation.
func (g *ToolGroupMessageItem) SelectChildPrev() bool {
	if !g.expanded || g.selectedChild == -1 {
		return false
	}
	g.selectedChild--
	g.invalidate()
	return true
}

// SelectChildFromBelow places the sub-cursor for a selection arriving
// from the item below: on the bottommost child while the one-liner
// level is open. A collapsed group, or a singleton whose only line is
// the call itself, keeps the cursor on the group row.
func (g *ToolGroupMessageItem) SelectChildFromBelow() {
	child := -1
	if g.showsChildRows() {
		child = len(g.tools) - 1
	}
	if g.selectedChild != child {
		g.selectedChild = child
		g.invalidate()
	}
}

// SelectedLineRange implements [list.SubSelectable]: the inclusive
// line range of the selection within the group's rendered output at
// the given width — the sub-cursor's child while it sits on one, the
// group row itself while the one-liner level is open. ok is false
// when the group renders as a single row (collapsed, or a singleton
// whose only line is the call), where item-level visibility already
// says it all.
func (g *ToolGroupMessageItem) SelectedLineRange(width int) (start, end int, ok bool) {
	if !g.showsChildRows() {
		return 0, 0, false
	}
	if g.selectedChild < 0 {
		return 0, 0, true
	}
	_, start, end = g.renderLines(width)
	return start, end, start >= 0
}

// ToggleSelectedChild expands or collapses the sub-cursor's child
// between its one-liner and full render. Reports whether a child was
// selected.
func (g *ToolGroupMessageItem) ToggleSelectedChild() bool {
	if g.selectedChild < 0 || g.selectedChild >= len(g.tools) {
		return false
	}
	g.tools[g.selectedChild].ToggleExpanded()
	g.invalidate()
	return true
}

// AddTool adds a tool call to the group.
func (g *ToolGroupMessageItem) AddTool(tool ToolMessageItem) {
	g.tools = append(g.tools, tool)
	g.invalidate()
}

// ChildTool returns the child tool call with the given ID, or nil.
func (g *ToolGroupMessageItem) ChildTool(id string) ToolMessageItem {
	for _, t := range g.tools {
		if t.ID() == id {
			return t
		}
	}
	return nil
}

// ToolChildren returns the group's tool calls.
func (g *ToolGroupMessageItem) ToolChildren() []ToolMessageItem {
	return g.tools
}

// Spinning implements [Animatable]: the group animates while any of its
// calls is still in flight.
func (g *ToolGroupMessageItem) Spinning() bool {
	for _, t := range g.tools {
		if a, ok := t.(Animatable); ok && a.Spinning() {
			return true
		}
	}
	return false
}

// Advance implements [Animatable]. Advances every spinning child in
// one frame, bumping the group's list-cache version: children are not
// list entries, so the list only sees this version.
func (g *ToolGroupMessageItem) Advance() bool {
	if !g.Spinning() {
		return false
	}
	changed := advanceNested(g.tools)
	if changed {
		g.Bump()
	}
	return changed
}

// Finished implements [list.Item]. The group stays unfrozen while any
// call can still settle.
func (g *ToolGroupMessageItem) Finished() bool {
	for _, t := range g.tools {
		if !t.Finished() {
			return false
		}
	}
	return true
}

// ToggleExpanded implements [Expandable] for the space key and mouse:
// on the group row it toggles between collapsed and the one-liner
// level; per-call expansion is driven by ToggleSelectedChild.
func (g *ToolGroupMessageItem) ToggleExpanded() bool {
	if g.expanded {
		g.collapse()
	} else {
		g.open()
	}
	return g.expanded
}

// ExpansionLevel implements [Expandable]: 1 while the group shows its
// one-liner level. Each call carries its own level.
func (g *ToolGroupMessageItem) ExpansionLevel() uint8 {
	return expansionLevel(g.expanded)
}

// SetExpansionLevel implements [Expandable]. It sets the group's own
// level only: its calls are items with levels of their own, which a
// restore sets by their own IDs, so the group must not second-guess
// them here. The space and escape keys, which do move the calls with
// the group, go through open and collapse.
func (g *ToolGroupMessageItem) SetExpansionLevel(level uint8) {
	if !applyExpansionLevel(&g.expanded, level) {
		return
	}
	if !g.expanded {
		g.selectedChild = -1
	}
	g.invalidate()
}

// SetSelectedChild places the sub-cursor on the given call, or on the
// group row for -1. It only lands where the cursor could have walked
// to: a call of an expanded multi-call group.
func (g *ToolGroupMessageItem) SetSelectedChild(child int) {
	if child < -1 || child >= len(g.tools) || child >= 0 && !g.showsChildRows() {
		return
	}
	if child != g.selectedChild {
		g.selectedChild = child
		g.invalidate()
	}
}

// open opens the one-liner level. A singleton is its own one-liner, so
// opening it goes straight to the call's full view.
func (g *ToolGroupMessageItem) open() {
	g.SetExpansionLevel(1)
	if len(g.tools) == 1 {
		g.tools[0].SetExpansionLevel(1)
	}
	g.invalidate()
}

// ExpandAndDescend opens the one-liner level and drops the sub-cursor
// onto the first call. Called from the group row.
func (g *ToolGroupMessageItem) ExpandAndDescend() {
	if !g.expanded {
		g.open()
	}
	if len(g.tools) > 1 {
		g.selectedChild = 0
	}
	g.invalidate()
}

// DigIn implements the enter key from anywhere inside the group: with
// the sub-cursor already on a call line it opens that call's full view
// (the cursor stays put); from the group row it expands and descends.
func (g *ToolGroupMessageItem) DigIn() {
	if g.selectedChild >= 0 && g.selectedChild < len(g.tools) {
		g.tools[g.selectedChild].SetExpansionLevel(1)
		g.invalidate()
		return
	}
	g.ExpandAndDescend()
}

// Ascend implements the escape key: with the sub-cursor on a call
// showing its full view, that call collapses and the cursor stays put
// (so escape can walk the remaining expanded calls one by one); any
// other escape on an expanded group collapses the group outright.
// Reports whether a level was consumed.
func (g *ToolGroupMessageItem) Ascend() bool {
	switch {
	case g.selectedChild >= 0 && g.selectedChild < len(g.tools) && ShowsFullView(g.tools[g.selectedChild]):
		g.tools[g.selectedChild].SetExpansionLevel(0)
	case g.expanded:
		g.collapse()
	default:
		return false
	}
	g.invalidate()
	return true
}

// collapse closes the group and resets every per-call expansion.
func (g *ToolGroupMessageItem) collapse() {
	g.SetExpansionLevel(0)
	for _, t := range g.tools {
		if ShowsFullView(t) {
			t.SetExpansionLevel(0)
		}
	}
	g.invalidate()
}

// ShowsFullView reports whether a tool call renders its full view
// rather than its one-liner: expanded and not in compact mode. The
// compact axis is the background task strip's rest state for its
// nested calls; transcript calls never carry it.
func ShowsFullView(t ToolMessageItem) bool {
	if c, ok := t.(Compactable); ok && c.IsCompact() {
		return false
	}
	return t.ExpansionLevel() != 0
}

// ToggleFullView flips a tool call between its full view and its
// one-liner - the one expansion move every surface shares. A call at
// rest opens, un-compacting the strip's nested calls; a full one
// collapses back and re-compactes if it was compact at rest. Reports
// whether the call shows its full view after the flip.
func ToggleFullView(t ToolMessageItem) bool {
	compact, isCompact := t.(Compactable)
	if ShowsFullView(t) {
		t.SetExpansionLevel(0)
		if isCompact {
			compact.SetCompact(true)
		}
		return false
	}
	if isCompact {
		compact.SetCompact(false)
	}
	t.SetExpansionLevel(1)
	return true
}

// NestedToolLines renders one nested tool call's lines under indent:
// its full view when it shows one, its one-liner otherwise, both at
// the body width the nesting level gives. Shared by the transcript's
// expanded groups and the background task strip's detail block.
func NestedToolLines(sty *styles.Styles, t ToolMessageItem, bodyWidth int, indent string, selected bool) []string {
	if !ShowsFullView(t) {
		return []string{indent + ToolOneLiner(sty, t, bodyWidth, selected)}
	}
	var lines []string
	for ln := range strings.SplitSeq(t.BodyRender(bodyWidth), "\n") {
		lines = append(lines, indent+ln)
	}
	return lines
}

// HandleMouseClick implements [MouseClickable]: any left click cycles
// the expansion levels, matching the space key.
func (g *ToolGroupMessageItem) HandleMouseClick(btn ansi.MouseButton, x, y int) bool {
	return btn == ansi.MouseLeft
}

// HandleKeyEvent implements [KeyEventHandler]: the copy binding copies
// the sub-cursor's child, or the whole run when the cursor is on the
// group row. See [ToolGroupMessageItem.formatGroupForCopy].
func (g *ToolGroupMessageItem) HandleKeyEvent(msg tea.KeyMsg, keys ItemKeymap) (bool, tea.Cmd) {
	if keys.MatchesCopy(msg) {
		return true, common.CopyToClipboard(g.formatGroupForCopy(), copyToastMessage)
	}
	return false, nil
}

// RawRender implements [MessageItem].
func (g *ToolGroupMessageItem) RawRender(width int) string {
	lines, _, _ := g.renderLines(width)
	return strings.Join(lines, "\n")
}

// renderLines builds the group's output lines and reports the inclusive
// line range occupied by the sub-cursor's child (-1s when the cursor
// sits on the group row), so Render can recolor that child's focus
// bar. Children live at the nesting level ToolBodyWidth defines: one
// indent column inside the group's bar, whether their one-liner or
// their full view.
func (g *ToolGroupMessageItem) renderLines(width int) (lines []string, selStart, selEnd int) {
	selStart, selEnd = -1, -1

	// A singleton renders as the call itself: the one-liner collapsed,
	// the full view at the chrome of a top-level call expanded - the
	// group's bar is that call's bar. A "Ran (1 tool calls)" header
	// says nothing the one-liner does not, and focus on the group is
	// selection of the call.
	if len(g.tools) == 1 {
		if g.expanded {
			return strings.Split(g.tools[0].RawRender(width), "\n"), -1, -1
		}
		return []string{g.oneLiner(g.tools[0], ToolBodyWidth(width, 0), g.focused)}, -1, -1
	}

	running := false
	cancelled := false
	failed := 0
	succeeded := 0
	for _, t := range g.tools {
		switch t.EffectiveStatus() {
		case ToolStatusRunning:
			running = true
		case ToolStatusError:
			failed++
		case ToolStatusCanceled:
			cancelled = true
		case ToolStatusSuccess:
			succeeded++
		}
	}
	// A single call returned above, so the count is always plural.
	calls := fmt.Sprintf("%d tool calls", len(g.tools))
	if failed > 0 {
		// The verb color already signals a partial failure; the count
		// says how much of the run to distrust without expanding it.
		calls += fmt.Sprintf(", %d failed", failed)
	}
	// The verb color alone says how the run went: pending for a live
	// run, normal once it has settled, error when it did not survive.
	// While the cursor sits on the group row the verb says it in color;
	// once it descends to a call the grey returns and the selected
	// call's own name takes the color instead.
	header := fmt.Sprintf("%s %s",
		GroupVerbStyle(g.sty, running, cancelled, failed, succeeded, g.focused && g.selectedChild < 0).Render(g.groupVerb()),
		g.sty.Tool.Body.Render("("+calls+")"))

	lines = append(lines, header)

	if g.expanded {
		// Children live one indent column inside the group's bar.
		bodyWidth := ToolBodyWidth(width, 1)
		for i, t := range g.tools {
			start := len(lines)
			lines = append(lines, NestedToolLines(g.sty, t, bodyWidth, toolNestIndentString, g.focused && i == g.selectedChild)...)
			if i == g.selectedChild {
				selStart, selEnd = start, len(lines)-1
			}
		}
		return lines, selStart, selEnd
	}

	// Collapsed: the header is the whole story. What the calls did
	// waits for an expansion.
	return lines, -1, -1
}

// Render renders the group with the shared per-line focus prefix. The
// sub-cursor's child gets the same bar in a brighter color instead of a
// text marker, so its lines stay aligned with its siblings'.
func (g *ToolGroupMessageItem) Render(width int) string {
	useCache := !g.Spinning()
	if useCache {
		if cached, ok := g.getCachedPrefixedRender(width, g.prefixKey()); ok {
			return cached
		}
	}
	prefix := g.sty.Messages.ToolCallBlurred.Render()
	selectedPrefix := prefix
	if g.focused {
		selectedPrefix = g.sty.Messages.ToolCallFocused.Render()
		// The bar marks one thing at a time. With the sub-cursor down
		// on a child, only that child carries it, in the same color the
		// outermost selection uses; the rest of the group goes bare
		// rather than keeping a second bar in a second color.
		if g.selectedChild < 0 {
			prefix = selectedPrefix
		}
	}
	lines, selStart, selEnd := g.renderLines(width)
	for i, ln := range lines {
		p := prefix
		if i >= selStart && i <= selEnd {
			p = selectedPrefix
		}
		lines[i] = p + ln
	}
	out := strings.Join(lines, "\n")
	if useCache {
		g.setCachedPrefixedRender(out, width, g.prefixKey())
	}
	return out
}

func (g *ToolGroupMessageItem) prefixKey() uint64 {
	if g.focused {
		return 1
	}
	return 0
}

// groupVerb names the run in the collapsed header and the copy header:
// "Running" while any call is still in flight, "Ran" once it has
// settled.
func (g *ToolGroupMessageItem) groupVerb() string {
	if g.Spinning() {
		return "Running"
	}
	return "Ran"
}

// GroupVerbStyle picks the style for a collapsed group's verb
// from the run's outcome: pending while in flight, error when every
// call failed, partial when some did, cancelled when nothing else
// happened, normal otherwise. A selected group row carries the status
// color; an unselected one stays in the understated grey. Shared with
// the background tasks strip, whose Agent rows use the same treatment.
func GroupVerbStyle(sty *styles.Styles, running, cancelled bool, failed, succeeded int, selected bool) lipgloss.Style {
	var style lipgloss.Style
	switch {
	case cancelled && failed == 0:
		return sty.Tool.NameCancelled
	case failed > 0 && succeeded == 0:
		return sty.Tool.NameError
	case failed > 0:
		return sty.Tool.NamePartial
	case selected:
		style = sty.Tool.NameNormalSelected
	default:
		style = sty.Tool.NameNormal
	}
	if running {
		if selected {
			return sty.Tool.NamePendingSelected
		}
		return sty.Tool.NamePending
	}
	return style
}

// ToolOneLiner renders one tool call as a single line: tool name,
// colored by status when selected and understated grey otherwise, and
// an argument summary, truncated to width. A running call uses the
// running color, matching the call's full render, which carries no
// spinner either. Shared by the transcript's expanded groups and the
// background task strip's nested lines.
func ToolOneLiner(sty *styles.Styles, t ToolMessageItem, width int, selected bool) string {
	name := toolNameStyle(sty, t.EffectiveStatus(), false, selected).Render(t.DisplayName())
	line := name
	if summary := ToolCallSummary(t.ToolCall()); summary != "" {
		line += " " + sty.Tool.Body.Render(summary)
	}
	return ansi.Truncate(line, max(width, 1), "…")
}

// oneLiner renders one tool call as a single line.
func (g *ToolGroupMessageItem) oneLiner(t ToolMessageItem, width int, selected bool) string {
	return ToolOneLiner(g.sty, t, width, selected)
}

// ToolCallSummary extracts a one-line argument summary from a tool call
// for display inside a collapsed group.
func ToolCallSummary(tc message.ToolCall) string {
	var params map[string]any
	if err := json.Unmarshal([]byte(tc.Input), &params); err != nil || len(params) == 0 {
		return ""
	}
	preferred := []string{"command", "file_path", "path", "pattern", "url", "query", "symbol", "name"}
	for _, key := range preferred {
		if v, ok := params[key].(string); ok && strings.TrimSpace(v) != "" {
			return FirstLine(v)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(params)) {
		if s, ok := params[key].(string); ok && strings.TrimSpace(s) != "" {
			return FirstLine(s)
		}
	}
	return ""
}

// FirstLine returns the first line of s, collapsed to one
// space-joined line.
func FirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.Join(strings.Fields(s), " ")
}
