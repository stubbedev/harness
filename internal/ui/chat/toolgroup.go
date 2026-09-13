package chat

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/anim"
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
	anim     *anim.Anim
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
	}
	g.anim = anim.New(anim.Settings{
		ID:         g.id,
		Size:       15,
		GradColorA: sty.WorkingGradFromColor,
		GradColorB: sty.WorkingGradToColor,
		LabelColor: sty.WorkingLabelColor,
	})
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

// Advance implements [Animatable]. Advances the group spinner and every
// spinning child in one frame, bumping the group's list-cache version:
// children are not list entries, so the list only sees this version.
func (g *ToolGroupMessageItem) Advance() bool {
	if !g.Spinning() {
		return false
	}
	changed := g.anim.Advance()
	changed = advanceNested(g.tools) || changed
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

// Ascend implements the escape key, moving out one level: a fully
// rendered call collapses to its one-liner, the sub-cursor returns to
// the group row, and an expanded group collapses. Reports whether a
// level was consumed.
func (g *ToolGroupMessageItem) Ascend() bool {
	switch {
	case g.selectedChild >= 0 && g.selectedChild < len(g.tools) && isToolExpanded(g.tools[g.selectedChild]):
		if e, ok := g.tools[g.selectedChild].(Expandable); ok {
			_ = e.ToggleExpanded()
		}
	case g.selectedChild >= 0:
		g.selectedChild = -1
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

// HandleKeyEvent implements [KeyEventHandler]: c/y copies every call in
// the group to the clipboard.
func (g *ToolGroupMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if k := key.String(); k == "c" || k == "y" {
		var b strings.Builder
		for i, t := range g.tools {
			if i > 0 {
				b.WriteString("\n\n")
			}
			if base, ok := t.(interface{ formatToolForCopy() string }); ok {
				b.WriteString(base.formatToolForCopy())
			}
		}
		return true, common.CopyToClipboard(b.String(), "Tool content copied to clipboard")
	}
	return false, nil
}

// RawRender implements [MessageItem].
func (g *ToolGroupMessageItem) RawRender(width int) string {
	contentWidth := max(width-MessageLeftPaddingTotal, 1)

	// A singleton renders as the call's own one-liner (or full render
	// once expanded) - a "Ran (1 tool calls)" header says nothing the
	// one-liner does not.
	if len(g.tools) == 1 {
		if g.expanded {
			return g.tools[0].Render(contentWidth)
		}
		return g.oneLiner(g.tools[0], contentWidth)
	}

	running := false
	failed := false
	cancelled := false
	for _, t := range g.tools {
		if a, ok := t.(Animatable); ok && a.Spinning() {
			running = true
		}
		if res := t.Result(); res != nil && res.IsError {
			failed = true
		}
		if t.Status() == ToolStatusCanceled {
			cancelled = true
		}
	}

	verb := "Ran"
	glyph := g.sty.Tool.IconSuccess.Render()
	if cancelled && !running {
		verb = "Ran"
		glyph = g.sty.Tool.IconCancelled.Render()
	}
	if failed {
		glyph = g.sty.Tool.IconError.Render()
	}
	if running {
		verb = "Running"
		glyph = g.anim.Render()
	}
	calls := fmt.Sprintf("%d tool calls", len(g.tools))
	if len(g.tools) == 1 {
		calls = "1 tool call"
	}
	header := fmt.Sprintf("%s %s %s",
		glyph,
		g.sty.Tool.NameNormal.Render(verb),
		g.sty.Tool.Body.Render("("+calls+")"))

	var lines []string
	lines = append(lines, header)

	if g.expanded {
		for i, t := range g.tools {
			indent := subItemIndentString
			if i == g.selectedChild && g.focused {
				indent = subItemSelectedString
			}
			if isToolExpanded(t) {
				for ln := range strings.SplitSeq(t.Render(contentWidth-subItemIndent), "\n") {
					lines = append(lines, indent+ln)
				}
			} else {
				lines = append(lines, indent+g.oneLiner(t, contentWidth-subItemIndent))
			}
		}
		return strings.Join(lines, "\n")
	}

	// Collapsed with work in flight: keep the live call visible beneath
	// the header so the user still sees what is happening right now.
	if running {
		if last := g.lastRunningTool(); last != nil {
			lines = append(lines, subItemIndentString+g.oneLiner(last, contentWidth-subItemIndent))
		}
	}
	return strings.Join(lines, "\n")
}

// Render renders the group with the shared per-line focus prefix.
func (g *ToolGroupMessageItem) Render(width int) string {
	useCache := !g.Spinning()
	if useCache {
		if cached, ok := g.getCachedPrefixedRender(width, g.prefixKey()); ok {
			return cached
		}
	}
	prefix := g.sty.Messages.ToolCallBlurred.Render()
	if g.focused {
		prefix = g.sty.Messages.ToolCallFocused.Render()
	}
	lines := strings.Split(g.RawRender(width), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
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

func (g *ToolGroupMessageItem) lastRunningTool() ToolMessageItem {
	for t := range slices.Backward(g.tools) {
		if a, ok := g.tools[t].(Animatable); ok && a.Spinning() {
			return g.tools[t]
		}
	}
	return nil
}

// oneLiner renders one tool call as a single line: status glyph, tool
// name and an argument summary, truncated to width. A running call gets
// the pending dot, not the tool's full scrambled spinner (a 15-cell
// animation with its own timer would crowd the line; the group header
// already carries the live animation).
func (g *ToolGroupMessageItem) oneLiner(t ToolMessageItem, width int) string {
	glyph := g.sty.Tool.IconSuccess.Render()
	if a, ok := t.(Animatable); ok && a.Spinning() {
		glyph = g.sty.Tool.IconPending.Render()
	} else if res := t.Result(); res != nil && res.IsError {
		glyph = g.sty.Tool.IconError.Render()
	} else if t.Status() == ToolStatusCanceled {
		glyph = g.sty.Tool.IconCancelled.Render()
	}
	name := g.sty.Tool.NameNormal.Render(PrettifyToolName(t.ToolCall().Name))
	line := glyph + " " + name
	if summary := ToolCallSummary(t.ToolCall()); summary != "" {
		line += " " + g.sty.Tool.Body.Render(summary)
	}
	return ansi.Truncate(line, max(width, 1), "…")
}

const (
	subItemIndent         = 2
	subItemIndentString   = "  "
	subItemSelectedString = "> "
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
			return firstLine(v)
		}
	}
	// Fallback: the first string value, whatever its key.
	for _, v := range params {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return firstLine(s)
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.Join(strings.Fields(s), " ")
}
