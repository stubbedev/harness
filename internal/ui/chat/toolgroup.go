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
// one collapsed row - "Ran (N tool calls)" - so the transcript is not
// dominated by tool bodies nobody asked for. Expansion has two levels:
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
	// row itself, >=0 on that child's line. Only meaningful while the
	// group is selected, expanded and the list is focused; cleared when
	// the group loses focus.
	selectedChild int
}

var (
	_ MessageItem         = (*ToolGroupMessageItem)(nil)
	_ Expandable          = (*ToolGroupMessageItem)(nil)
	_ Animatable          = (*ToolGroupMessageItem)(nil)
	_ KeyEventHandler     = (*ToolGroupMessageItem)(nil)
	_ list.MouseClickable = (*ToolGroupMessageItem)(nil)
	_ ToolGroupContainer  = (*ToolGroupMessageItem)(nil)
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
		cachedMessageItem:    &cachedMessageItem{},
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

// SetFocused implements [list.Focusable], clearing the sub-cursor when
// the selection moves off the group.
func (g *ToolGroupMessageItem) SetFocused(focused bool) {
	if !focused && g.selectedChild != -1 {
		g.selectedChild = -1
		g.clearCache()
	}
	g.focusableMessageItem.SetFocused(focused)
}

// ExpandedLevel reports whether the group shows its one-liner level.
func (g *ToolGroupMessageItem) ExpandedLevel() bool { return g.expanded }

// FullyRenderedChildren counts the calls currently showing their full
// view, so callers can tell whether a level change shrank the render.
func (g *ToolGroupMessageItem) FullyRenderedChildren() int {
	n := 0
	for _, t := range g.tools {
		if isToolExpanded(t) {
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
	g.clearCache()
	g.Bump()
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
	g.clearCache()
	g.Bump()
	return true
}

// ToggleSelectedChild expands or collapses the sub-cursor's child
// between its one-liner and full render. Reports whether a child was
// selected.
func (g *ToolGroupMessageItem) ToggleSelectedChild() bool {
	if g.selectedChild < 0 || g.selectedChild >= len(g.tools) {
		return false
	}
	child := g.tools[g.selectedChild]
	if e, ok := child.(Expandable); ok {
		_ = e.ToggleExpanded()
	}
	g.clearCache()
	g.Bump()
	return true
}

// AddTool adds a tool call to the group.
func (g *ToolGroupMessageItem) AddTool(tool ToolMessageItem) {
	g.tools = append(g.tools, tool)
	g.clearCache()
	g.Bump()
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
		g.expanded = true
		if len(g.tools) == 1 {
			// A singleton is its own one-liner; expanding it goes
			// straight to the call's full view.
			if e, ok := g.tools[0].(Expandable); ok && !isToolExpanded(g.tools[0]) {
				_ = e.ToggleExpanded()
			}
		}
	}
	g.clearCache()
	g.Bump()
	return g.expanded
}

// ExpandAndDescend opens the one-liner level and drops the sub-cursor
// onto the first call. Called from the group row.
func (g *ToolGroupMessageItem) ExpandAndDescend() {
	if !g.expanded {
		g.expanded = true
		if len(g.tools) == 1 {
			if e, ok := g.tools[0].(Expandable); ok && !isToolExpanded(g.tools[0]) {
				_ = e.ToggleExpanded()
			}
		}
	}
	if len(g.tools) > 1 {
		g.selectedChild = 0
	}
	g.clearCache()
	g.Bump()
}

// DigIn implements the enter key from anywhere inside the group: with
// the sub-cursor already on a call line it opens that call's full view
// (the cursor stays put); from the group row it expands and descends.
func (g *ToolGroupMessageItem) DigIn() {
	if g.selectedChild >= 0 && g.selectedChild < len(g.tools) {
		child := g.tools[g.selectedChild]
		if !isToolExpanded(child) {
			if e, ok := child.(Expandable); ok {
				_ = e.ToggleExpanded()
			}
		}
		g.clearCache()
		g.Bump()
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
	case g.selectedChild >= 0 && g.selectedChild < len(g.tools) && isToolExpanded(g.tools[g.selectedChild]):
		if e, ok := g.tools[g.selectedChild].(Expandable); ok {
			_ = e.ToggleExpanded()
		}
	case g.expanded:
		g.collapse()
	default:
		return false
	}
	g.clearCache()
	g.Bump()
	return true
}

// collapse closes the group and resets every per-call expansion.
func (g *ToolGroupMessageItem) collapse() {
	g.expanded = false
	g.selectedChild = -1
	for _, t := range g.tools {
		if e, ok := t.(Expandable); ok && isToolExpanded(t) {
			_ = e.ToggleExpanded()
		}
	}
}

// isToolExpanded reports whether a tool item currently renders its full
// content. Items without the probe always count as expanded.
func isToolExpanded(t ToolMessageItem) bool {
	base, ok := t.(interface{ Expanded() bool })
	if !ok {
		return true
	}
	return base.Expanded()
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
// sits on the group row or the group is unfocused), so Render can
// recolor that child's focus bar. Children render at the one-liner
// indentation whether collapsed or expanded: the expanded view uses
// RawRender, which omits the per-item left prefix Render would add.
func (g *ToolGroupMessageItem) renderLines(width int) (lines []string, selStart, selEnd int) {
	contentWidth := max(width-MessageLeftPaddingTotal, 1)
	selStart, selEnd = -1, -1

	// A singleton renders as the call's own one-liner (or full render
	// once expanded) - a "Ran (1 tool calls)" header says nothing the
	// one-liner does not. The row is the whole group, so focus on the
	// group is selection of the call.
	if len(g.tools) == 1 {
		if g.expanded {
			return strings.Split(g.tools[0].Render(contentWidth), "\n"), -1, -1
		}
		return []string{g.oneLiner(g.tools[0], contentWidth, g.focused)}, -1, -1
	}

	running := false
	cancelled := false
	failed := 0
	succeeded := 0
	for _, t := range g.tools {
		if a, ok := t.(Animatable); ok && a.Spinning() {
			running = true
		}
		if res := t.Result(); res != nil && res.IsError {
			failed++
		} else if t.Status() == ToolStatusCanceled {
			cancelled = true
		} else {
			succeeded++
		}
	}
	calls := fmt.Sprintf("%d tool calls", len(g.tools))
	if len(g.tools) == 1 {
		calls = "1 tool call"
	}
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
		groupVerbStyle(g.sty, running, cancelled, failed, succeeded, g.focused && g.selectedChild < 0).Render("Ran"),
		g.sty.Tool.Body.Render("("+calls+")"))

	lines = append(lines, header)

	if g.expanded {
		for i, t := range g.tools {
			start := len(lines)
			if isToolExpanded(t) {
				for ln := range strings.SplitSeq(t.RawRender(contentWidth), "\n") {
					lines = append(lines, subItemIndentString+ln)
				}
			} else {
				lines = append(lines, subItemIndentString+g.oneLiner(t, contentWidth-subItemIndent, g.focused && i == g.selectedChild))
			}
			if g.focused && i == g.selectedChild {
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

// groupVerbStyle picks the style for a collapsed group's "Ran" verb
// from the run's outcome: pending while in flight, error when every
// call failed, partial when some did, cancelled when nothing else
// happened, normal otherwise. A selected group row carries the status
// color; an unselected one stays in the understated grey.
func groupVerbStyle(sty *styles.Styles, running, cancelled bool, failed, succeeded int, selected bool) lipgloss.Style {
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
	status := ToolStatusSuccess
	if a, ok := t.(Animatable); ok && a.Spinning() {
		status = ToolStatusRunning
	} else if res := t.Result(); res != nil && res.IsError {
		status = ToolStatusError
	} else if t.Status() == ToolStatusCanceled {
		status = ToolStatusCanceled
	}
	name := toolNameStyle(sty, status, false, selected).Render(ToolDisplayName(t.ToolCall()))
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

const (
	subItemIndent       = 1
	subItemIndentString = " "
)

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
