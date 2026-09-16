package chat

import (
	"encoding/json"
	"strings"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// -----------------------------------------------------------------------------
// Shell Tool
// -----------------------------------------------------------------------------

// ShellToolMessageItem is a message item that represents a shell tool call.
type ShellToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*ShellToolMessageItem)(nil)

// NewShellToolMessageItem creates a new [ShellToolMessageItem].
func NewShellToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
	workingDir string,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &ShellToolRenderContext{workingDir: workingDir}, canceled)
}

// ShellToolRenderContext renders shell tool messages.
type ShellToolRenderContext struct {
	workingDir string
}

// RenderTool implements the [ToolRenderer] interface.
func (b *ShellToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		// The command shows as the model types it.
		if cmd, ok := partialStringField(opts.ToolCall.Input, "command"); ok && strings.TrimSpace(cmd) != "" {
			return pendingToolDetail(sty, "Shell", pendingDetail(cmd, cappedWidth-24), opts.Anim, opts.Compact)
		}
		return pendingTool(sty, "Shell", opts.Anim, opts.Compact)
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
	if !opts.ExpandedContent {
		cmd = strings.ReplaceAll(cmd, "\n", " ")
	}
	cmd = strings.ReplaceAll(cmd, "\t", "    ")
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

	header := toolHeader(sty, opts.Status, "Shell", cappedWidth, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
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

	bodyWidth := cappedWidth - toolBodyLeftPaddingTotal
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, output, bodyWidth, opts.ExpandedContent))
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
