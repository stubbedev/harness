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
type ShellToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (b *ShellToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	if opts.IsPending() {
		var pending tools.ShellParams
		_ = json.Unmarshal([]byte(opts.ToolCall.Input), &pending)
		// The command shows as the model types it.
		cmd, _ := partialStringField(opts.ToolCall.Input, "command")
		if pending.Command != "" {
			cmd = pending.Command
		}
		if pending.RunInBackground || partialBoolField(opts.ToolCall.Input, "run_in_background") {
			// The job id only exists once the call returns; until then
			// the header still names the call as a job, not a session.
			return backgroundShellView(sty, width, opts, cmd, "")
		}
		if strings.TrimSpace(cmd) != "" {
			return pendingToolView(sty, opts, opts.Name, common.StripShellDisplayPrefix(cmd), width)
		}
		return pendingToolView(sty, opts, opts.Name, "", width)
	}

	var params tools.ShellParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		params.Command = "failed to parse command"
	}

	var meta tools.ShellResponseMetadata
	if opts.HasResult() {
		_ = json.Unmarshal([]byte(opts.Result.Metadata), &meta)
	}

	if params.RunInBackground || meta.Background {
		return backgroundShellView(sty, width, opts, params.Command, meta.JobID)
	}

	// Whatever was typed at the session, or a poll.
	cmd := params.Command
	if cmd == "" {
		cmd = "(poll session)"
	}
	cmd = shellCommandDisplay(sty, cmd)
	toolParams := []string{cmd}
	// Show how long the command ran (or has been running) beside the
	// command: bash calls can take minutes and the bare header gives no
	// sense of it.
	if badge := toolDurationBadge(opts.Elapsed); badge != "" {
		toolParams = append(toolParams, "", badge)
	}

	header := toolHeader(sty, opts.Name, width, opts, toolParams...)
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

	body := toolOutputTerminalContent(sty, output, width, opts.ExpandedContent)
	return joinToolParts(header, body)
}

// backgroundShellView renders a call that started a background job
// instead of typing into a terminal session. The header names the job
// and carries the command, so the call never reads as a session
// command; the start acknowledgement is already summarised by the
// header, so it only shows once the call is expanded.
func backgroundShellView(sty *styles.Styles, width int, opts *ToolRenderOpts, cmd, jobID string) string {
	label := "Job"
	if jobID != "" {
		label += " " + jobID
	}
	toolParams := []string{shellCommandDisplay(sty, cmd)}
	if badge := toolDurationBadge(opts.Elapsed); badge != "" {
		toolParams = append(toolParams, "", badge)
	}
	header := strings.TrimSuffix(toolHeader(sty, label, width, opts, toolParams...), " ")
	if opts.Compact {
		return header
	}
	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}
	if !opts.HasResult() || !opts.ExpandedContent || opts.Result.Content == "" {
		return header
	}
	return joinToolParts(header, toolOutputTerminalContent(sty, opts.Result.Content, width, opts.ExpandedContent))
}

// shellCommandDisplay renders a command for a header: the display
// prefix peeled and the remainder highlighted as bash.
func shellCommandDisplay(sty *styles.Styles, cmd string) string {
	cmd = common.StripShellDisplayPrefix(cmd)
	if highlighted, err := common.SyntaxHighlightLexerName(sty, cmd, "bash", nil); err == nil {
		return highlighted
	}
	return cmd
}

// joinToolParts joins header and body directly: the body's own padding
// provides the visual indent, with no blank separator line.
func joinToolParts(header, body string) string {
	if body == "" {
		return header
	}
	return header + "\n" + body
}
