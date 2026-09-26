package chat

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/hooks"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/stringext"
	"github.com/stubbedev/harness/internal/ui/anim"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// responseContextHeight limits the number of lines displayed in tool output.
const responseContextHeight = 10

// ToolStatus represents the current state of a tool call.
type ToolStatus int

const (
	ToolStatusRunning ToolStatus = iota
	ToolStatusSuccess
	ToolStatusError
	ToolStatusCanceled
)

// ToolMessageItem represents a tool call message in the chat UI.
type ToolMessageItem interface {
	MessageItem
	Expandable

	// BodyRender renders the call's full view at exactly the given
	// body width, with no left chrome of its own; callers derive the
	// width from ToolBodyWidth for the call's nesting level.
	BodyRender(bodyWidth int) string

	ToolCall() message.ToolCall
	SetToolCall(tc message.ToolCall)
	SetResult(res *message.ToolResult)
	Result() *message.ToolResult
	MessageID() string
	SetMessageID(id string)
	// EffectiveStatus is the call's status as every view shows it: the
	// result decides once there is one, otherwise it is canceled or
	// still running.
	EffectiveStatus() ToolStatus
}

// Compactable is an interface for tool items that can render in a compacted mode.
// When compact mode is enabled, tools render as a compact single-line header.
type Compactable interface {
	SetCompact(compact bool)
	IsCompact() bool
}

// LiveDiagnosticsSetter is implemented by tool items whose view depends on
// the language servers' current state. The UI model pushes the freshest
// per-file counts whenever an LSP event or the TTL backstop refreshes its
// memoized state, and the item invalidates its cached render, so a report
// shown in the transcript never claims a problem still stands after the
// servers have said it is gone.
type LiveDiagnosticsSetter interface {
	SetLiveDiagnostics(live map[string]lsp.DiagnosticCounts)
}

// ItemEnv carries the live bindings message items need to render
// session state that lives outside the message history. A nil env (or
// nil field) is valid: history rebuilds render static fallbacks.
type ItemEnv struct {
	// WaitingAgents reports how many subagents are still running in the
	// session an agent-wait call belongs to. It is read at render time,
	// so the count tracks agents finishing while the call is pending.
	WaitingAgents func() int
}

// ToolRenderOpts contains the data needed to render a tool call.
type ToolRenderOpts struct {
	ToolCall        message.ToolCall
	Result          *message.ToolResult
	ExpandedContent bool
	Compact         bool
	Status          ToolStatus
	// StartedAt is when the tool call started rendering live. Zero for
	// items restored from history, where the real start time is unknown,
	// so no elapsed time is shown for them.
	StartedAt time.Time
	// Elapsed is how long the tool call has been (or was) running.
	// Zero when the start time is unknown (restored items).
	Elapsed time.Duration
	// WaitingAgents is the live count of still-running subagents for an
	// agent-wait call, resolved from the item's env at render time.
	WaitingAgents int
}

// IsPending returns true if the tool call is still pending (not finished and
// not canceled).
func (o *ToolRenderOpts) IsPending() bool {
	return !o.ToolCall.Finished && !o.IsCanceled()
}

// IsCanceled returns true if the tool status is canceled.
func (o *ToolRenderOpts) IsCanceled() bool {
	return o.Status == ToolStatusCanceled
}

// HasResult returns true if the result is not nil.
func (o *ToolRenderOpts) HasResult() bool {
	return o.Result != nil
}

// HasEmptyResult returns true if the result is nil or has empty content.
func (o *ToolRenderOpts) HasEmptyResult() bool {
	return o.Result == nil || o.Result.Content == ""
}

// ToolRenderer represents an interface for rendering tool calls.
type ToolRenderer interface {
	RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string
}

// baseToolMessageItem represents a tool call message that can be displayed in the UI.
type baseToolMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	toolRenderer ToolRenderer
	toolCall     message.ToolCall
	result       *message.ToolResult
	messageID    string
	canceled     bool
	isCompact    bool
	// waitingAgents resolves the live subagent count for an agent-wait
	// call's header; nil for history rebuilds.
	waitingAgents func() int

	sty             *styles.Styles
	anim            *anim.Anim
	expandedContent bool
	startedAt       time.Time
	finishedAt      time.Time
}

var _ Expandable = (*baseToolMessageItem)(nil)

// markRestored clears startedAt for items rebuilt from session history:
// the real start time is unknown there, and a constructor timestamp would
// restart the timer from zero and misreport the elapsed time.
func (t *baseToolMessageItem) markRestored() {
	if !t.startedAt.IsZero() {
		t.startedAt = time.Time{}
		t.Bump()
	}
}

// newBaseToolMessageItem is the internal constructor for base tool message items.
func newBaseToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	toolRenderer ToolRenderer,
	canceled bool,
	env *ItemEnv,
) *baseToolMessageItem {
	v := list.NewVersioned()
	t := &baseToolMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        newCachedMessageItem(v),
		focusableMessageItem:     newFocusableMessageItem(v),
		sty:                      sty,
		toolRenderer:             toolRenderer,
		toolCall:                 toolCall,
		result:                   result,
		canceled:                 canceled,
		startedAt:                time.Now(),
	}
	if env != nil {
		t.waitingAgents = env.WaitingAgents
	}
	t.anim = anim.New(anim.Settings{
		ID: toolCall.ID,
		// The anim is the item's advance clock, not something rendered:
		// a pending call shows no spinner, but its waiting state line
		// reports elapsed time, so the item must keep re-rendering while
		// it runs. Reads startedAt lazily: it is cleared for restored
		// items, whose elapsed time stays unknown.
		PulseGlyphs: anim.DefaultPulseGlyphs,
	})

	return t
}

// NewToolMessageItem creates a new [ToolMessageItem] based on the tool call name.
//
// It returns a specific tool message item type if implemented, otherwise it
// returns a generic tool message item. The messageID is the ID of the assistant
// message containing this tool call. env may be nil; it carries the live
// bindings (see [ItemEnv]) that only live views can supply.
func NewToolMessageItem(
	sty *styles.Styles,
	messageID string,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
	env ...*ItemEnv,
) ToolMessageItem {
	var item ToolMessageItem
	switch toolCall.Name {
	case tools.DiagnosticsToolName, tools.LSPToolName:
		item = newLSPToolMessageItem(sty, toolCall, result, canceled)
	default:
		item = newBaseToolMessageItem(sty, toolCall, result, toolRendererFor(toolCall), canceled, firstEnv(env))
	}
	item.SetMessageID(messageID)
	return item
}

// firstEnv resolves the optional env to nil when absent.
func firstEnv(env []*ItemEnv) *ItemEnv {
	if len(env) == 0 {
		return nil
	}
	return env[0]
}

// toolRendererFor returns the renderer for a tool call. The whole call
// is the key: the agent tool renders as a wait while it is in its
// waiting form (no prompt) and never renders at all as a dispatch, so
// the name alone cannot decide.
func toolRendererFor(toolCall message.ToolCall) ToolRenderer {
	switch toolCall.Name {
	case tools.ShellToolName:
		return &ShellToolRenderContext{}
	case tools.ViewToolName:
		return &ViewToolRenderContext{}
	case tools.WriteToolName:
		return &WriteToolRenderContext{}
	case tools.EditToolName:
		return &EditToolRenderContext{}
	case tools.FetchToolName:
		return &FetchToolRenderContext{}
	case tools.WebSearchToolName:
		return &WebSearchToolRenderContext{}
	case tools.QuestionToolName:
		return &QuestionToolRenderContext{}
	case agent.AgentToolName:
		if agent.IsAgentWaitCall(toolCall.Name, toolCall.Input) {
			return &WaitToolRenderContext{}
		}
	}
	if strings.HasPrefix(toolCall.Name, "mcp_") {
		return &MCPToolRenderContext{}
	}
	return &GenericToolRenderContext{}
}

// IsSubagentTool reports whether a tool call dispatches a subagent
// (agent or research). Subagent calls do not render in the transcript;
// they live in the background tasks strip.
func IsSubagentTool(name string) bool {
	return name == agent.AgentToolName || name == tools.ResearchToolName
}

// IsInternalContextTool reports whether a tool call is harness-internal
// plumbing rather than work the user asked for: skill_search and
// tool_search defer skill and MCP tool loading out of the system
// prompt, and send_message is subagent-to-orchestrator mail. These
// calls stay in the message history the model sees, but never render
// in the transcript, the expanded task views, or the export.
func IsInternalContextTool(name string) bool {
	return name == agent.SkillSearchToolName ||
		name == agent.ToolSearchToolName ||
		name == agent.SendMessageToolName
}

// RendersInTranscript reports whether a tool call appears in the
// transcript, and therefore in the export, which mirrors the screen.
// Subagent dispatches live in the background tasks strip instead; the
// dispatcher tool's waiting form does render — it is the turn's
// visible "waiting for N agents" state — and context plumbing never
// renders. Single source for the extraction and export filters, so the
// two cannot drift.
func RendersInTranscript(name, input string) bool {
	if IsInternalContextTool(name) {
		return false
	}
	return !IsSubagentTool(name) || agent.IsAgentWaitCall(name, input)
}

// SetCompact implements the Compactable interface.
func (t *baseToolMessageItem) SetCompact(compact bool) {
	if t.isCompact == compact {
		return
	}
	t.isCompact = compact
	t.invalidate()
}

// IsCompact reports whether the item renders in compact (one-line) mode.
func (t *baseToolMessageItem) IsCompact() bool {
	return t.isCompact
}

// ID returns the unique identifier for this tool message item.
func (t *baseToolMessageItem) ID() string {
	return t.toolCall.ID
}

// Spinning implements [Animatable].
func (t *baseToolMessageItem) Spinning() bool {
	return t.isSpinning()
}

// Advance implements [Animatable].
//
// Bumps the F6 list-cache version so the next draw re-renders this
// item: a running call's waiting state line reports elapsed time, and
// without the bump the list cache would serve the previously rendered
// frame indefinitely and the timer would appear frozen.
func (t *baseToolMessageItem) Advance() bool {
	if !t.isSpinning() || !t.anim.Advance() {
		return false
	}
	t.Bump()
	return true
}

// RawRender implements [MessageItem]: the call's full view at the
// top-level body width, the focus bar Render would draw in front of
// it removed.
func (t *baseToolMessageItem) RawRender(width int) string {
	return t.BodyRender(ToolBodyWidth(width, 0))
}

// toolFullWidth reports whether a call renders at the full body width.
// Only diffs do; everything else is capped at [maxTextWidth] for
// readability. The action of an lsp call is only known once its input
// has streamed in, so this is decided per render.
func toolFullWidth(tc message.ToolCall) bool {
	switch tc.Name {
	case tools.EditToolName:
		return true
	case tools.LSPToolName:
		return lspAction(tc) == "replace_symbol"
	}
	return false
}

// BodyRender renders the call's full view in the given body width, with
// no left chrome of its own: the bar and any group indent around it
// belong to the caller, which derives the width from [ToolBodyWidth] for
// the call's nesting level. This is the one place the readability cap is
// applied, so renderers treat the width they get as final.
func (t *baseToolMessageItem) BodyRender(bodyWidth int) string {
	if !toolFullWidth(t.toolCall) {
		bodyWidth = min(bodyWidth, maxTextWidth)
	}
	content, height, ok := t.getCachedRender(bodyWidth)
	// if we are spinning or there is no cache rerender
	if !ok || t.isSpinning() {
		opts := ToolRenderOpts{
			ToolCall:        t.toolCall,
			Result:          t.result,
			ExpandedContent: t.expandedContent,
			Compact:         t.isCompact,
			Status:          t.EffectiveStatus(),
			StartedAt:       t.startedAt,
			Elapsed:         t.elapsed(),
		}
		// Read at render time so a pending wait's header tracks agents
		// finishing while it runs. The item re-renders per tick until
		// its result lands (see isSpinning), so the count stays fresh.
		if t.waitingAgents != nil {
			opts.WaitingAgents = t.waitingAgents()
		}
		content = t.toolRenderer.RenderTool(t.sty, bodyWidth, &opts)

		// Prepend hook indicator if hooks ran for this tool call.
		if t.result != nil {
			if hookLine := toolOutputHookIndicator(t.sty, t.result.Metadata, bodyWidth); hookLine != "" {
				content = hookLine + "\n\n" + content
			}
		}

		height = lipgloss.Height(content)
		// cache the rendered content
		t.setCachedRender(content, bodyWidth, height)
	}

	return t.renderHighlighted(content, bodyWidth, height)
}

// Render renders the tool message item at the given width.
func (t *baseToolMessageItem) Render(width int) string {
	// Cache the prefixed output keyed by (width, prefix variant).
	// Bypass the cache while spinning (RawRender output is
	// frame-dependent) or while a highlight range is active.
	useCache := !t.isSpinning() && !t.isHighlighted()
	var key uint64
	switch {
	case t.isCompact:
		key = 2
	case t.focused:
		key = 1
	default:
		key = 0
	}
	if useCache {
		if cached, ok := t.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	var prefix string
	if t.isCompact {
		prefix = t.sty.Messages.ToolCallCompact.Render()
	} else if t.focused {
		prefix = t.sty.Messages.ToolCallFocused.Render()
	} else {
		prefix = t.sty.Messages.ToolCallBlurred.Render()
	}
	lines := strings.Split(t.RawRender(width), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	out := strings.Join(lines, "\n")
	if useCache {
		t.setCachedPrefixedRender(out, width, key)
	}
	return out
}

// ToolCall returns the tool call associated with this message item.
func (t *baseToolMessageItem) ToolCall() message.ToolCall {
	return t.toolCall
}

// SetToolCall sets the tool call associated with this message item.
func (t *baseToolMessageItem) SetToolCall(tc message.ToolCall) {
	// Capture the end time on the live finished transition so the tool's
	// total duration can be shown after completion.
	if tc.Finished && !t.toolCall.Finished && !t.startedAt.IsZero() {
		t.finishedAt = time.Now()
	}
	t.toolCall = tc
	t.invalidate()
}

// elapsed returns how long the tool call has been (or was) running. Zero
// when the start time is unknown (restored items).
func (t *baseToolMessageItem) elapsed() time.Duration {
	if t.startedAt.IsZero() {
		return 0
	}
	end := t.finishedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(t.startedAt)
}

// SetResult sets the tool result associated with this message item.
func (t *baseToolMessageItem) SetResult(res *message.ToolResult) {
	t.result = res
	t.invalidate()
}

// Result returns the tool result recorded for this call, if any.
func (t *baseToolMessageItem) Result() *message.ToolResult {
	return t.result
}

// MessageID returns the ID of the message containing this tool call.
func (t *baseToolMessageItem) MessageID() string {
	return t.messageID
}

// SetMessageID sets the ID of the message containing this tool call.
// MessageID is metadata only and does not affect the rendered output,
// so we deliberately do not bump the version here.
func (t *baseToolMessageItem) SetMessageID(id string) {
	t.messageID = id
}

// EffectiveStatus implements [ToolMessageItem].
func (t *baseToolMessageItem) EffectiveStatus() ToolStatus {
	switch {
	case t.result != nil && t.result.IsError:
		return ToolStatusError
	case t.result != nil:
		return ToolStatusSuccess
	case t.canceled:
		return ToolStatusCanceled
	default:
		return ToolStatusRunning
	}
}

// isSpinning returns true if the tool should show animation.
func (t *baseToolMessageItem) isSpinning() bool {
	// Keep animating while waiting for the tool result too, so the
	// "Waiting for tool response for Xs" label keeps ticking.
	return (!t.toolCall.Finished || t.result == nil) && !t.canceled
}

// ToggleExpanded implements [Expandable]: it flips the call between its
// one-liner and its full output.
func (t *baseToolMessageItem) ToggleExpanded() bool {
	t.SetExpansionLevel(expansionLevel(!t.expandedContent))
	return t.expandedContent
}

// ExpansionLevel implements [Expandable]: 1 while the call shows its
// full output.
func (t *baseToolMessageItem) ExpansionLevel() uint8 {
	return expansionLevel(t.expandedContent)
}

// SetExpansionLevel implements [Expandable].
func (t *baseToolMessageItem) SetExpansionLevel(level uint8) {
	if applyExpansionLevel(&t.expandedContent, level) {
		t.invalidate()
	}
}

// Finished implements list.Item. A tool call is freezable once the
// tool call itself is marked finished AND a result has been recorded
// (or it has been canceled).
func (t *baseToolMessageItem) Finished() bool {
	if t.isSpinning() {
		return false
	}
	return t.canceled || (t.toolCall.Finished && t.result != nil)
}

// HandleMouseClick implements MouseClickable.
func (t *baseToolMessageItem) HandleMouseClick(btn ansi.MouseButton, x, y int) bool {
	return btn == ansi.MouseLeft
}

// HandleKeyEvent implements KeyEventHandler.
func (t *baseToolMessageItem) HandleKeyEvent(msg tea.KeyMsg, keys ItemKeymap) (bool, tea.Cmd) {
	if keys.MatchesCopy(msg) {
		return true, common.CopyToClipboard(t.formatToolForCopy(), copyToastMessage)
	}
	return false, nil
}

// pendingToolView renders a tool call that has not finished yet. It
// uses the standard header form - the status-colored name plus
// whatever of the argument is known so far - so a running call reads
// like a settled one and the working spinner never rides the header
// line. Liveness comes from the waiting state line, which renders
// exactly as it would for a finished call still awaiting its result,
// so a call with no output yet remains expandable.
func pendingToolView(sty *styles.Styles, opts *ToolRenderOpts, name, detail string, width int) string {
	header := strings.TrimSuffix(toolHeader(sty, name, width, opts, detail), " ")
	if opts.Compact {
		return header
	}
	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}
	return header
}

// renderStandardTool renders the shape every standard tool shares: the
// pending view, then the header, then - unless the call is compact, in an
// early state (error, canceled, running) or without result content - the
// body. params is only called for a settled call, since a pending one may
// still be streaming its input; returning false renders an "Invalid
// parameters" error instead. body renders the settled result; nil renders
// it as plain text, and an empty string leaves the header alone.
func renderStandardTool(
	sty *styles.Styles,
	width int,
	opts *ToolRenderOpts,
	name string,
	params func() ([]string, bool),
	body func() string,
) string {
	if opts.IsPending() {
		return pendingToolView(sty, opts, name, "", width)
	}
	toolParams, ok := params()
	if !ok {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, width)
	}
	header := toolHeader(sty, name, width, opts, toolParams...)
	if opts.Compact {
		return header
	}
	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}
	if opts.HasEmptyResult() {
		return header
	}
	if body == nil {
		return joinToolParts(header, toolPlainBody(sty, opts, width))
	}
	return joinToolParts(header, body())
}

// toolPlainBody renders the result content as plain text.
func toolPlainBody(sty *styles.Styles, opts *ToolRenderOpts, width int) string {
	return sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, width, opts.ExpandedContent))
}

// jsonToolParams renders a call's whole input as its one header param, for
// tools with no dedicated renderer.
func jsonToolParams(input string) ([]string, bool) {
	var params map[string]any
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, false
	}
	if len(params) == 0 {
		return nil, true
	}
	parsed, _ := json.Marshal(params)
	return []string{string(parsed)}, true
}

// waitingForToolMessage builds the "Waiting for tool response..." label,
// including how long the tool has been running and, when a turn is
// active, the total elapsed turn time.
func waitingForToolMessage(opts *ToolRenderOpts) string {
	msg := "Waiting for tool response"
	if !opts.StartedAt.IsZero() {
		msg += fmt.Sprintf(" for %s", common.FormatDuration(time.Since(opts.StartedAt)))
	}
	if total := common.Elapsed(); total != "" {
		msg += fmt.Sprintf(" (%s total)", total)
	}
	return msg + "..."
}

// toolEarlyStateContent handles error/cancelled/pending states before content rendering.
// Returns the rendered output and true if early state was handled.
func toolEarlyStateContent(sty *styles.Styles, opts *ToolRenderOpts, width int) (string, bool) {
	var msg string
	switch opts.Status {
	case ToolStatusError:
		msg = toolErrorContent(sty, opts.Result, width)
	case ToolStatusCanceled:
		msg = sty.Tool.StateCancelled.Render("Canceled.")
	case ToolStatusRunning:
		msg = sty.Tool.StateWaiting.Render(waitingForToolMessage(opts))
	default:
		return "", false
	}
	return msg, true
}

// toolErrorContent formats an error message with an ERROR or WARN tag.
func toolErrorContent(sty *styles.Styles, result *message.ToolResult, width int) string {
	if result == nil {
		return ""
	}
	errContent := strings.ReplaceAll(result.Content, "\n", " ")
	if result.Canceled {
		deniedTag := sty.Tool.WarnTag.Render("WARN")
		deniedTagWidth := lipgloss.Width(deniedTag)
		errContent = ansi.Truncate(errContent, width-deniedTagWidth-3, "…")
		return fmt.Sprintf("%s %s", deniedTag, sty.Tool.WarnMessage.Render(errContent))
	}
	errTag := sty.Tool.ErrorTag.Render("ERROR")
	tagWidth := lipgloss.Width(errTag)
	errContent = ansi.Truncate(errContent, width-tagWidth-3, "…")
	return fmt.Sprintf("%s %s", errTag, sty.Tool.ErrorMessage.Render(errContent))
}

// toolNameStyle returns the tool-name style for a call's status and
// selection. Unselected one-liners stay in the understated grey so
// tool calls recede behind the chat messages; a selected call, and
// every full render (which is the expanded view), carries its status
// color - green while running, blue when done,
// red on failure, yellow for a partially failed run. Nested calls
// (rendered inside a group) use the nested variants. Canceled stays
// muted either way.
func toolNameStyle(sty *styles.Styles, status ToolStatus, nested, selected bool) lipgloss.Style {
	switch status {
	case ToolStatusError:
		return sty.Tool.NameError
	case ToolStatusCanceled:
		return sty.Tool.NameCancelled
	case ToolStatusRunning:
		if selected {
			return sty.Tool.NamePendingSelected
		}
		return sty.Tool.NamePending
	default:
		switch {
		case nested && selected:
			return sty.Tool.NameNestedSelected
		case nested:
			return sty.Tool.NameNested
		case selected:
			return sty.Tool.NameNormalSelected
		default:
			return sty.Tool.NameNormal
		}
	}
}

// oneLine flattens text to a single space-joined line: whitespace
// runs, tabs, and newlines all collapse to single spaces.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// toolParamList formats tool parameters as "main (key=value, ...)".
// The text is always a single line: whitespace runs (including
// newlines in multi-line commands) collapse to single spaces, and
// anything past the width is cut with an ellipsis.
func toolParamList(sty *styles.Styles, params []string, width int) string {
	// minSpaceForMainParam is the min space required for the main param
	// if this is less that the value set we will only show the main param nothing else
	const minSpaceForMainParam = 30
	if len(params) == 0 {
		return ""
	}

	mainParam := oneLine(params[0])
	if isHTTPURL(mainParam) {
		// Carry the URL's OSC 8 link inside the styled param so terminals
		// that support hyperlinks open it on click. The link wraps the
		// bare text; the caller's style still colors the whole line.
		mainParam = lipgloss.NewStyle().Hyperlink(mainParam).Render(mainParam)
	}

	// Build key=value pairs from remaining params (consecutive key, value pairs).
	var kvPairs []string
	for i := 1; i+1 < len(params); i += 2 {
		if params[i+1] != "" {
			kvPairs = append(kvPairs, fmt.Sprintf("%s=%s",
				oneLine(params[i]), oneLine(params[i+1])))
		}
	}

	// Try to include key=value pairs if there's enough space.
	output := mainParam
	if len(kvPairs) > 0 {
		partsStr := strings.Join(kvPairs, ", ")
		if remaining := width - lipgloss.Width(partsStr) - 3; remaining >= minSpaceForMainParam {
			output = fmt.Sprintf("%s (%s)", mainParam, partsStr)
		}
	}

	if width >= 0 {
		output = ansi.Truncate(output, width, "…")
	}
	return sty.Tool.ParamMain.Render(output)
}

// isHTTPURL reports whether s is a bare http(s) URL worth linking.
// Strict on purpose: whitespace anywhere means the text only looks like
// a URL, and linking it would capture the junk into the link target.
func isHTTPURL(s string) bool {
	if strings.ContainsAny(s, " \t\n\r") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// toolHeader builds the tool header line: "ToolName params...", with
// the name colored by status. The parameter text is always a single
// line, ellipsis-truncated to the remaining width.
func toolHeader(sty *styles.Styles, name string, width int, opts *ToolRenderOpts, params ...string) string {
	// The full render is the expanded view, so its name always says
	// the status in color; the grey belongs to the collapsed rows.
	toolName := toolNameStyle(sty, opts.Status, opts.Compact, true).Render(name)
	prefix := toolName + " "
	remainingWidth := width - lipgloss.Width(prefix)
	return prefix + toolParamList(sty, params, remainingWidth)
}

// toolOutputPlainContent renders plain text with optional expansion support.
func toolOutputPlainContent(sty *styles.Styles, content string, width int, expanded bool) string {
	content = stringext.NormalizeSpace(content)
	content = common.StripCursorControl(content)
	content = common.RemapANSI16(content, sty.ANSI)
	lines := strings.Split(content, "\n")

	maxLines := responseContextHeight
	if expanded {
		maxLines = len(lines) // Show all
	}

	var out []string
	for i, ln := range lines {
		if i >= maxLines {
			break
		}
		ln = " " + ln
		if lipgloss.Width(ln) > width {
			ln = ansi.Truncate(ln, width, "…")
		}
		out = append(out, sty.Tool.ContentLine.Width(width).Render(ln))
	}

	wasTruncated := len(lines) > responseContextHeight

	if !expanded && wasTruncated {
		out = append(out, sty.Tool.ContentTruncation.
			Width(width).
			Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-responseContextHeight)))
	}

	return strings.Join(out, "\n")
}

// toolOutputCodeContent renders code with syntax highlighting and line numbers.
func toolOutputCodeContent(sty *styles.Styles, path, content string, offset, width int, expanded bool) string {
	content = stringext.NormalizeSpace(content)

	lines := strings.Split(content, "\n")
	maxLines := responseContextHeight
	if expanded {
		maxLines = len(lines)
	}

	// Truncate if needed.
	displayLines := lines
	if len(lines) > maxLines {
		displayLines = lines[:maxLines]
	}

	bg := sty.Tool.ContentCodeBg
	highlighted, _ := common.SyntaxHighlight(sty, strings.Join(displayLines, "\n"), path, bg)
	highlightedLines := strings.Split(highlighted, "\n")

	// Calculate line number width.
	maxLineNumber := len(displayLines) + offset
	maxDigits := getDigits(maxLineNumber)
	numFmt := fmt.Sprintf("%%%dd", maxDigits)

	bodyWidth := width
	codeWidth := bodyWidth - maxDigits

	var out []string
	for i, ln := range highlightedLines {
		lineNum := sty.Tool.ContentLineNumber.Render(fmt.Sprintf(numFmt, i+1+offset))

		// Truncate accounting for padding that will be added.
		ln = ansi.Truncate(ln, codeWidth-sty.Tool.ContentCodeLine.GetHorizontalPadding(), "…")

		codeLine := sty.Tool.ContentCodeLine.
			Width(codeWidth).
			Render(ln)

		out = append(out, lipgloss.JoinHorizontal(lipgloss.Left, lineNum, codeLine))
	}

	// Add truncation message if needed.
	if len(lines) > maxLines && !expanded {
		out = append(
			out, sty.Tool.ContentCodeTruncation.
				Width(width).
				Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-maxLines)),
		)
	}

	return sty.Tool.Body.Render(strings.Join(out, "\n"))
}

// toolOutputImageContent renders image data with size info.
func toolOutputImageContent(sty *styles.Styles, data, mediaType string) string {
	dataSize := len(data) * 3 / 4
	sizeStr := formatSize(dataSize)

	return sty.Tool.Body.Render(fmt.Sprintf(
		"%s %s %s %s",
		sty.Tool.ResourceLoadedText.Render("Loaded Image"),
		sty.Tool.ResourceLoadedIndicator.Render(styles.ArrowRightIcon),
		sty.Tool.MediaType.Render(mediaType),
		sty.Tool.ResourceSize.Render(sizeStr),
	))
}

// toolOutputSkillContent renders a skill loaded indicator.
func toolOutputSkillContent(sty *styles.Styles, name, description string) string {
	return sty.Tool.Body.Render(fmt.Sprintf(
		"%s %s %s %s",
		sty.Tool.ResourceLoadedText.Render("Loaded Skill"),
		sty.Tool.ResourceLoadedIndicator.Render(styles.ArrowRightIcon),
		sty.Tool.ResourceName.Render(name),
		sty.Tool.ResourceSize.Render(description),
	))
}

// toolOutputHookIndicator renders hook indicator lines from tool metadata.
// Returns empty string if no hook metadata is present. Hook names are
// sanitized (newlines replaced with ¶) and truncated to fit the available
// horizontal space.
func toolOutputHookIndicator(sty *styles.Styles, metadata string, width int) string {
	if metadata == "" {
		return ""
	}
	var meta struct {
		Hook *hooks.HookMetadata `json:"hook"`
	}
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil || meta.Hook == nil {
		return ""
	}
	h := meta.Hook
	if len(h.Hooks) == 0 {
		return ""
	}

	// Sanitize names (replace newlines with ¶) and compute max widths
	// for the name, matcher, and detail columns so they align. The name
	// column is capped at maxHookNameWidth characters.
	const maxHookNameWidth = 30
	sanitizedNames := make([]string, len(h.Hooks))
	details := make([]string, len(h.Hooks))
	maxNameWidth := 0
	maxMatcherWidth := 0
	maxDetailWidth := 0
	for i, hi := range h.Hooks {
		sanitizedNames[i] = strings.ReplaceAll(hi.Name, "\n", "¶")
		w := lipgloss.Width(sty.Tool.HookName.Render(sanitizedNames[i]))
		if w > maxNameWidth {
			maxNameWidth = w
		}
		if hi.Matcher != "" {
			mw := lipgloss.Width(sty.Tool.HookMatcher.Render(hi.Matcher))
			if mw > maxMatcherWidth {
				maxMatcherWidth = mw
			}
		}
		details[i] = hookDetail(sty, hi)
		if dw := lipgloss.Width(details[i]); dw > maxDetailWidth {
			maxDetailWidth = dw
		}
	}

	if maxNameWidth > maxHookNameWidth {
		maxNameWidth = maxHookNameWidth
	}

	// Cap the name column so the widest line still fits in width. The
	// per-line layout is:
	//   "Hook " + name(padded) + [" " + matcher(padded)] + " → " + detail
	if width > 0 {
		fixed := lipgloss.Width(sty.Tool.HookLabel.Render("Hook")) + 1
		if maxMatcherWidth > 0 {
			fixed += 1 + maxMatcherWidth
		}
		fixed += 1 + lipgloss.Width(sty.Tool.HookArrow.Render(styles.ArrowRightIcon)) + 1
		fixed += maxDetailWidth
		if budget := width - fixed; budget < maxNameWidth {
			maxNameWidth = max(1, budget)
		}
	}

	var lines []string
	for i, hi := range h.Hooks {
		name := truncateHookName(sanitizedNames[i], maxNameWidth)
		lines = append(lines, renderHookLine(sty, hi, name, details[i], maxNameWidth, maxMatcherWidth))
	}
	return strings.Join(lines, "\n")
}

// truncateHookName truncates a hook name to fit within maxWidth cells,
// using left-truncation for absolute paths (e.g. `…/format.sh`) and
// right-truncation for everything else. Left-truncation is only applied
// when the name looks unambiguously like a path: absolute, single-line,
// and contains no spaces.
func truncateHookName(name string, maxWidth int) string {
	if ansi.StringWidth(name) <= maxWidth {
		return name
	}
	if isLikelyPath(name) {
		// ansi.TruncateLeft removes n graphemes from the start; pick n
		// so the result plus the "…" prefix fits in maxWidth.
		n := ansi.StringWidth(name) - maxWidth + 1
		return ansi.TruncateLeft(name, n, "…")
	}
	return ansi.Truncate(name, maxWidth, "…")
}

// isLikelyPath reports whether s looks unambiguously like a filesystem
// path, suitable for left-truncation. We accept absolute paths and
// relative paths that contain a separator and no shell-ish characters.
func isLikelyPath(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\n¶'\"|&;<>$`*?(){}[]\\") {
		return false
	}
	if filepath.IsAbs(s) {
		return true
	}
	return strings.Contains(s, "/")
}

// renderHookLine renders a single hook indicator line with aligned columns.
func renderHookLine(sty *styles.Styles, hi hooks.HookInfo, rawName, detail string, maxNameWidth, maxMatcherWidth int) string {
	name := sty.Tool.HookName.Render(rawName)
	namePad := strings.Repeat(" ", max(0, maxNameWidth-lipgloss.Width(name)))

	var matcherPart string
	if maxMatcherWidth > 0 {
		if hi.Matcher != "" {
			matcher := sty.Tool.HookMatcher.Render(hi.Matcher)
			matcherPad := strings.Repeat(" ", maxMatcherWidth-lipgloss.Width(matcher))
			matcherPart = " " + matcher + matcherPad
		} else {
			matcherPart = " " + strings.Repeat(" ", maxMatcherWidth)
		}
	}

	labelStyle := sty.Tool.HookLabel
	arrowStyle := sty.Tool.HookArrow
	if hi.Decision == "deny" {
		labelStyle = sty.Tool.HookDeniedLabel
		arrowStyle = sty.Tool.HookDeniedLabel
	}

	return fmt.Sprintf(
		"%s %s%s%s %s %s",
		labelStyle.Render("Hook"),
		name,
		namePad,
		matcherPart,
		arrowStyle.Render(styles.ArrowRightIcon),
		detail,
	)
}

// hookDetail returns the styled detail text for a single hook result.
func hookDetail(sty *styles.Styles, hi hooks.HookInfo) string {
	const (
		okMessage      = "OK"
		denialMessage  = "Denied"
		rewroteMessage = "Rewrote Output"
	)
	switch hi.Decision {
	case "deny":
		if hi.Reason != "" {
			return sty.Tool.HookDenied.Render(denialMessage) + " " + sty.Tool.HookDeniedReason.Render(hi.Reason)
		}
		return sty.Tool.HookDenied.Render(denialMessage)
	case "allow":
		result := sty.Tool.HookOK.Render(okMessage)
		if hi.InputRewrite {
			result += " " + sty.Tool.HookRewrote.Render(rewroteMessage)
		}
		return result
	default:
		result := sty.Tool.HookOK.Render(okMessage)
		if hi.InputRewrite {
			result += " " + sty.Tool.HookRewrote.Render(rewroteMessage)
		}
		return result
	}
}

// getDigits returns the number of digits in a number.
func getDigits(n int) int {
	if n == 0 {
		return 1
	}
	if n < 0 {
		n = -n
	}
	digits := 0
	for n > 0 {
		n /= 10
		digits++
	}
	return digits
}

// formatSize formats byte size into human readable format.
func formatSize(bytes int) string {
	const (
		kb = 1024
		mb = kb * 1024
	)
	switch {
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// toolOutputDiffContent renders a diff between old and new content.
func toolOutputDiffContent(sty *styles.Styles, file, oldContent, newContent string, width int, expanded bool) string {
	bodyWidth := width

	formatter := common.DiffFormatter(sty).
		Before(file, oldContent).
		After(file, newContent).
		Width(bodyWidth)

	// Use split view for wide terminals.
	if width > maxTextWidth {
		formatter = formatter.Split()
	}

	formatted := formatter.String()
	lines := strings.Split(formatted, "\n")

	// Truncate if needed.
	maxLines := responseContextHeight
	if expanded {
		maxLines = len(lines)
	}

	if len(lines) > maxLines && !expanded {
		truncMsg := sty.Tool.DiffTruncation.
			Width(bodyWidth).
			Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-maxLines))
		formatted = strings.Join(lines[:maxLines], "\n") + "\n" + truncMsg
	}

	return sty.Tool.Body.Render(formatted)
}

// formatTimeout converts timeout seconds to a duration string (e.g., "30s").
// Returns empty string if timeout is 0.
func formatTimeout(timeout int) string {
	if timeout == 0 {
		return ""
	}
	return fmt.Sprintf("%ds", timeout)
}

// toolOutputEditDiffContent renders a diff with optional failed edits note.
func toolOutputEditDiffContent(sty *styles.Styles, file string, meta tools.EditResponseMetadata, totalEdits, width int, expanded bool) string {
	bodyWidth := width

	formatter := common.DiffFormatter(sty).
		Before(file, meta.OldContent).
		After(file, meta.NewContent).
		Width(bodyWidth)

	// Use split view for wide terminals.
	if width > maxTextWidth {
		formatter = formatter.Split()
	}

	formatted := formatter.String()
	lines := strings.Split(formatted, "\n")

	// Truncate if needed.
	maxLines := responseContextHeight
	if expanded {
		maxLines = len(lines)
	}

	if len(lines) > maxLines && !expanded {
		truncMsg := sty.Tool.DiffTruncation.
			Width(bodyWidth).
			Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-maxLines))
		formatted = truncMsg + "\n" + strings.Join(lines[:maxLines], "\n")
	}

	// Add failed edits note if any exist.
	if len(meta.EditsFailed) > 0 {
		noteTag := sty.Tool.NoteTag.Render("Note")
		noteMsg := fmt.Sprintf("%d of %d edits succeeded", meta.EditsApplied, totalEdits)
		note := fmt.Sprintf("%s %s", noteTag, sty.Tool.NoteMessage.Render(noteMsg))
		formatted = formatted + "\n\n" + note
	}

	return sty.Tool.Body.Render(formatted)
}

// toolOutputMarkdownContent renders markdown content with optional truncation.
func toolOutputMarkdownContent(sty *styles.Styles, content string, width int, expanded bool) string {
	content = stringext.NormalizeSpace(content)

	// Cap width for readability.
	if width > maxTextWidth {
		width = maxTextWidth
	}

	renderer := common.QuietMarkdownRenderer(sty, width)
	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	rendered, err := renderer.Render(content)
	mu.Unlock()
	if err != nil {
		return toolOutputPlainContent(sty, content, width, expanded)
	}

	lines := strings.Split(rendered, "\n")
	maxLines := responseContextHeight
	if expanded {
		maxLines = len(lines)
	}

	var out []string
	for i, ln := range lines {
		if i >= maxLines {
			break
		}
		out = append(out, ln)
	}

	if len(lines) > maxLines && !expanded {
		out = append(
			out, sty.Tool.ContentTruncation.
				Width(width).
				Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-maxLines)),
		)
	}

	return sty.Tool.Body.Render(strings.Join(out, "\n"))
}

// ToolDisplayName returns the label the UI shows for a tool call. It is
// the one place tool names become display text, so a full renderer's
// header, a collapsed group's one-liner, the background task strip and
// the clipboard heading cannot drift apart. The lsp tool folds several
// actions into one wire name; the label follows the action it carries.
func ToolDisplayName(tc message.ToolCall) string {
	if name := lspDisplayName(tc); name != "" {
		return name
	}
	switch tc.Name {
	case tools.ShellToolName:
		return "Shell"
	case tools.ViewToolName:
		return "View"
	case tools.WriteToolName:
		return "Write"
	case tools.EditToolName:
		return "Edit"
	case tools.FetchToolName:
		return "Fetch"
	case tools.WebSearchToolName:
		return "Search"
	case tools.QuestionToolName:
		return "Question"
	case tools.DiagnosticsToolName:
		return "Diagnostics"
	}
	if server, tool, ok := splitMCPName(tc.Name); ok {
		return server + " -> " + tool
	}
	return humanizedToolName(tc.Name)
}

// lspDisplayName labels an lsp call by its action, reading the same
// field the renderer dispatch in lspToolRenderer.pick reads, so a call
// renders and summarizes under one name. Empty when tc is not an lsp
// call.
func lspDisplayName(tc message.ToolCall) string {
	if tc.Name != tools.LSPToolName {
		return ""
	}
	switch lspAction(tc) {
	case "references":
		return "Find References"
	case "definition":
		return "Find Definition"
	case "rename":
		return "Rename Symbol"
	case "replace_symbol":
		return "Replace Symbol"
	case "call_hierarchy":
		return "Call Hierarchy"
	case "symbols":
		return "List Symbols"
	case "restart":
		return "Restart LSP"
	default:
		return "Diagnostics"
	}
}

// lspAction reads the action an lsp call dispatches. Unparsable input
// falls through to the diagnostics default in both consumers.
func lspAction(tc message.ToolCall) string {
	var params struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal([]byte(tc.Input), &params)
	return params.Action
}

// splitMCPName splits the "mcp_server_tool" wire name into its human
// parts. The mcp renderer styles these same parts with color; this is
// the plain form one-liners and copy headings show.
func splitMCPName(name string) (server, tool string, ok bool) {
	if !strings.HasPrefix(name, "mcp_") {
		return "", "", false
	}
	parts := strings.SplitN(name, "_", 3)
	if len(parts) != 3 {
		return "", "", false
	}
	return humanizedToolName(parts[1]), humanizedToolName(parts[2]), true
}
