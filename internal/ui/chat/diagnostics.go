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

// LSPToolMessageItem is a message item that represents an lsp tool call
// (or a legacy standalone diagnostics call).
type LSPToolMessageItem struct {
	*baseToolMessageItem
	renderer *lspToolRenderer
}

var (
	_ ToolMessageItem       = (*LSPToolMessageItem)(nil)
	_ LiveDiagnosticsSetter = (*LSPToolMessageItem)(nil)
)

// newLSPToolMessageItem creates a new [LSPToolMessageItem].
func newLSPToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	r := &lspToolRenderer{}
	return &LSPToolMessageItem{
		baseToolMessageItem: newBaseToolMessageItem(sty, toolCall, result, r, canceled),
		renderer:            r,
	}
}

// SetLiveDiagnostics records the language servers' current per-file state and
// invalidates the cached render. A diagnostics item reports a point in time;
// without the live overlay a file the agent has since fixed keeps showing its
// old errors as the transcript's last word. See [LiveDiagnosticsSetter].
func (d *LSPToolMessageItem) SetLiveDiagnostics(live map[string]lsp.DiagnosticCounts) {
	rc := &d.renderer.diagnostics
	if maps.Equal(rc.live, live) {
		return
	}
	rc.live = live
	// Only a diagnostics run draws the overlay; other actions keep their
	// cached render.
	if _, ok := d.renderer.pick(d.toolCall).(*DiagnosticsToolRenderContext); ok {
		d.clearCache()
		d.Bump()
	}
}

// lspToolRenderer picks the action's renderer on every render. The actions
// were separate tools once and kept their own renderers when they were
// folded into one; the transcript still shows a rename differently from a
// diagnostics run. The pick cannot happen once at construction: a streamed
// call is created with empty input, before its action is known.
type lspToolRenderer struct {
	diagnostics DiagnosticsToolRenderContext
}

// RenderTool implements the [ToolRenderer] interface.
func (r *lspToolRenderer) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return r.pick(opts.ToolCall).RenderTool(sty, width, opts)
}

func (r *lspToolRenderer) pick(tc message.ToolCall) ToolRenderer {
	switch lspAction(tc) {
	case "references":
		return &ReferencesToolRenderContext{}
	case "definition":
		return &DefinitionToolRenderContext{}
	case "rename":
		return &RenameToolRenderContext{}
	case "replace_symbol":
		return &ReplaceSymbolToolRenderContext{}
	case "call_hierarchy":
		return &CallHierarchyToolRenderContext{}
	case "symbols":
		return &SymbolsToolRenderContext{}
	case "restart":
		return &LSPRestartToolRenderContext{}
	default:
		return &r.diagnostics
	}
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
	var params tools.DiagnosticsParams
	return renderStandardTool(sty, width, opts, ToolDisplayName(opts.ToolCall), func() ([]string, bool) {
		_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)
		// Show "project" if no file path, otherwise show the file path.
		if params.FilePath == "" {
			return []string{"project"}, true
		}
		return []string{fsext.PrettyPath(params.FilePath)}, true
	}, func() string {
		body := toolPlainBody(sty, opts, width)
		if note := d.liveNote(params.FilePath); note != "" {
			body = joinToolParts(body, sty.Tool.TodoStatusNote.Render(note))
		}
		return body
	})
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
