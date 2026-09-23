package chat

import (
	"encoding/json"
	"strings"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// -----------------------------------------------------------------------------
// Shell Tool
// -----------------------------------------------------------------------------

// ShellToolRenderContext renders shell tool messages.
type ShellToolRenderContext struct {
	workingDir string
}

// RenderTool implements the [ToolRenderer] interface.
func (b *ShellToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	if opts.IsPending() {
		// The command shows as the model types it.
		if cmd, ok := partialStringField(opts.ToolCall.Input, "command"); ok && strings.TrimSpace(cmd) != "" {
			return pendingToolView(sty, opts, ToolDisplayName(opts.ToolCall), cmd, width)
		}
		return pendingToolView(sty, opts, ToolDisplayName(opts.ToolCall), "", width)
	}

	var params tools.ShellParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		params.Command = "failed to parse command"
	}

	var meta tools.ShellResponseMetadata
	if opts.HasResult() {
		_ = json.Unmarshal([]byte(opts.Result.Metadata), &meta)
	}

	// Whatever was typed at the session, or a poll.
	cmd := params.Command
	if cmd == "" {
		cmd = "(poll session)"
	}
	cmd = common.StripShellDisplayPrefix(cmd, b.workingDir)
	if highlighted, err := common.SyntaxHighlightLexerName(sty, cmd, "bash", nil); err == nil {
		cmd = highlighted
	}
	toolParams := []string{cmd}
	// Show how long the command ran (or has been running) beside the
	// command: bash calls can take minutes and the bare header gives no
	// sense of it.
	if opts.Elapsed > 0 {
		toolParams = append(toolParams, "took", common.FormatDuration(opts.Elapsed))
	}

	header := toolHeader(sty, ToolDisplayName(opts.ToolCall), width, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}

	if !opts.HasResult() {
		return header
	}

	output := meta.Output
	if output == "" && opts.Result.Content != tools.ShellNoOutput {
		output = opts.Result.Content
	}
	if output == "" {
		return header
	}

	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, output, width, opts.ExpandedContent))
	return joinToolParts(header, body)
}

// joinToolParts joins header and body directly: the body's own padding
// provides the visual indent, with no blank separator line.
func joinToolParts(header, body string) string {
	if body == "" {
		return header
	}
	return header + "\n" + body
}
