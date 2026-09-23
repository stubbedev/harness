package chat

import (
	"encoding/json"
	"maps"
	"strconv"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// -----------------------------------------------------------------------------
// Diagnostics Tool
// -----------------------------------------------------------------------------

// DiagnosticsToolMessageItem is a message item that represents a diagnostics tool call.
type DiagnosticsToolMessageItem struct {
	*baseToolMessageItem
}

var (
	_ ToolMessageItem       = (*DiagnosticsToolMessageItem)(nil)
	_ LiveDiagnosticsSetter = (*DiagnosticsToolMessageItem)(nil)
)

// NewDiagnosticsToolMessageItem creates a new [DiagnosticsToolMessageItem].
func NewDiagnosticsToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return &DiagnosticsToolMessageItem{
		baseToolMessageItem: newBaseToolMessageItem(sty, toolCall, result, &DiagnosticsToolRenderContext{}, canceled),
	}
}

// SetLiveDiagnostics records the language servers' current per-file state and
// invalidates the cached render. A diagnostics item reports a point in time;
// without the live overlay a file the agent has since fixed keeps showing its
// old errors as the transcript's last word. See [LiveDiagnosticsSetter].
func (d *DiagnosticsToolMessageItem) SetLiveDiagnostics(live map[string]lsp.DiagnosticCounts) {
	rc, ok := d.toolRenderer.(*DiagnosticsToolRenderContext)
	if !ok || maps.Equal(rc.live, live) {
		return
	}
	rc.live = live
	d.clearCache()
	d.Bump()
}

// DiagnosticsToolRenderContext renders diagnostics tool messages. It carries
// the live per-file diagnostic counts pushed in by the UI model, so the
// rendered item can say whether the problems it reported still stand.
type DiagnosticsToolRenderContext struct {
	// live holds the most recent per-file counts, or nil while nothing has
	// been pushed. A nil map must not read as "everything resolved": it
	// means the state is unknown and no overlay is drawn.
	live map[string]lsp.DiagnosticCounts
}

// RenderTool implements the [ToolRenderer] interface.
func (d *DiagnosticsToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingToolView(sty, opts, ToolDisplayName(opts.ToolCall), "", width)
	}

	var params tools.DiagnosticsParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	// Show "project" if no file path, otherwise show the file path.
	mainParam := "project"
	if params.FilePath != "" {
		mainParam = fsext.PrettyPath(params.FilePath)
	}

	header := toolHeader(sty, opts.Status, ToolDisplayName(opts.ToolCall), cappedWidth, opts, mainParam)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	bodyWidth := cappedWidth
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
	if note := d.liveNote(params.FilePath); note != "" {
		body = joinToolParts(body, sty.Tool.TodoStatusNote.Render(note))
	}
	return joinToolParts(header, body)
}

// liveNote describes the file's current state next to the report, when the
// servers have said anything about it since. An empty path means the report
// covered the whole project, so the totals are summed across every file. A
// file the servers are not tracking is skipped: their silence about it is not
// evidence it is clean.
func (d *DiagnosticsToolRenderContext) liveNote(path string) string {
	if d.live == nil {
		return ""
	}
	if path == "" {
		counts := totalsFor(d.live, "")
		if liveCountNote(counts) == "" {
			return "resolved — no diagnostics remain in the project"
		}
		return liveCountNote(counts) + " still open in the project"
	}
	counts, known := d.live[path]
	if !known {
		return ""
	}
	if note := liveCountNote(counts); note != "" {
		return note + " still open in " + fsext.PrettyPath(path)
	}
	return "resolved — no diagnostics remain for " + fsext.PrettyPath(path)
}

// totalsFor sums the per-file counts, either for one path or across the whole
// map when path is empty.
func totalsFor(live map[string]lsp.DiagnosticCounts, path string) lsp.DiagnosticCounts {
	var total lsp.DiagnosticCounts
	for file, counts := range live {
		if path != "" && file != path {
			continue
		}
		total.Error += counts.Error
		total.Warning += counts.Warning
		total.Information += counts.Information
		total.Hint += counts.Hint
	}
	return total
}

// liveCountNote renders the non-zero severities of a count, e.g.
// "1 error, 2 warnings". Empty when nothing is wrong.
func liveCountNote(counts lsp.DiagnosticCounts) string {
	note := ""
	add := func(n int, singular, plural string) {
		if n == 0 {
			return
		}
		if note != "" {
			note += ", "
		}
		word := plural
		if n == 1 {
			word = singular
		}
		note += strconv.Itoa(n) + " " + word
	}
	add(counts.Error, "error", "errors")
	add(counts.Warning, "warning", "warnings")
	add(counts.Information, "info", "info")
	add(counts.Hint, "hint", "hints")
	return note
}
