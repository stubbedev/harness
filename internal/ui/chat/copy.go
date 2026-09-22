package chat

// Clipboard formatting for transcript items.
//
// Every copyable item renders to a small self-describing Markdown
// document: a heading naming the item, optional sub-sections, and a
// fence around anything reproduced verbatim. Four rules hold across
// every item type, and new item types are expected to keep them:
//
//   - Fenced. Verbatim content always sits inside a fence, tagged with
//     a language whenever one can be derived, so a paste into a
//     Markdown document survives intact.
//   - Deterministic. No map iteration order reaches the output. Object
//     payloads are re-marshalled with sorted keys rather than dumped
//     through Go's map formatting.
//   - Capped. Anything that can be arbitrarily large is truncated at
//     [copyContentLimit] with a visible marker, rather than putting a
//     multi-megabyte file on the clipboard.
//   - View-independent. What an item copies never depends on whether it
//     is collapsed, expanded, scrolled or truncated on screen. Copy
//     follows the selection, not the viewport.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/diff"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/xchroma"
)

const (
	// copyContentLimit caps the size of any single verbatim block
	// (a file's contents, a command's output, a fetched page). Beyond
	// it the block is cut at a line boundary and marked. The limit is
	// per block, not per document: a tool call with a large input and
	// a large result can exceed it in total.
	copyContentLimit = 256 * 1024

	// copyToastMessage is the single confirmation every copy reports.
	// Distinguishing "Message" from "Tool content" from "Shell output"
	// told the user nothing they did not already know from what they
	// had selected.
	copyToastMessage = "Copied to clipboard"

	// copyBaseDepth is the heading level a standalone item copies at.
	// Children of a group render one level deeper so the group header
	// stays above them.
	copyBaseDepth = 2
)

// copyHeading renders a Markdown heading of the given depth.
func copyHeading(depth int, text string) string {
	return strings.Repeat("#", max(depth, 1)) + " " + text
}

// copyJoin joins non-empty sections with a blank line between them.
func copyJoin(parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

// copyFence wraps body in a fenced block tagged with lang. The fence is
// widened past the longest backtick run inside body, so content that is
// itself fenced (a Markdown file, an agent's report) does not terminate
// the block early.
func copyFence(lang, body string) string {
	body = copyTruncate(body)
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			longest = max(longest, run)
			continue
		}
		run = 0
	}
	fence := strings.Repeat("`", max(3, longest+1))
	body = strings.TrimRight(body, "\n")
	return fence + lang + "\n" + body + "\n" + fence
}

// copyTruncate cuts s to [copyContentLimit], preferring a line boundary
// and never splitting a rune, and appends a marker naming what was
// dropped. Content within the limit is returned unchanged.
func copyTruncate(s string) string {
	if len(s) <= copyContentLimit {
		return s
	}
	cut := copyContentLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if nl := strings.LastIndexByte(s[:cut], '\n'); nl > 0 {
		cut = nl
	}
	return s[:cut] + fmt.Sprintf("\n... truncated, %d more bytes", len(s)-cut)
}

// copyFenceLang derives a fence language from a file path. It goes
// through the same memoized Chroma lookup the syntax highlighter uses,
// so every language the UI can highlight also gets a tagged fence -
// the previous hand-written map covered fourteen extensions.
func copyFenceLang(path string) string {
	if path == "" {
		return ""
	}
	lexer := xchroma.MatchLexer(filepath.Base(path))
	if lexer == nil {
		return ""
	}
	cfg := lexer.Config()
	lang := strings.ToLower(cfg.Name)
	if len(cfg.Aliases) > 0 {
		lang = cfg.Aliases[0]
	}
	// Chroma's catch-all lexers carry no information a fence can use.
	switch lang {
	case "plaintext", "text", "fallback", "":
		return ""
	}
	if strings.ContainsAny(lang, " `\n") {
		return ""
	}
	return lang
}

// copyField renders a `**Label:** value` parameter line.
func copyField(label string, value any) string {
	return fmt.Sprintf("**%s:** %v", label, value)
}

// copyJSON re-marshals a JSON document with sorted keys and indentation,
// fenced as JSON. It is the deterministic replacement for dumping a
// decoded `map[string]any` through %v, whose key order varied between
// copies of the very same tool call. Input that is not valid JSON is
// returned in an untagged fence instead.
func copyJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return copyFence("", raw)
	}
	// encoding/json sorts object keys on the way out, which is what
	// makes this deterministic; the decode above is only there to
	// reject non-JSON.
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return copyFence("", raw)
	}
	return copyFence("json", string(out))
}

// -----------------------------------------------------------------------------
// Messages
// -----------------------------------------------------------------------------

// copyMessageText concatenates every text part of a message. Assistant
// turns can carry more than one - a text part before a tool call and
// another after it - and copying only the first silently dropped the
// answer in exactly the turns where it mattered most. Thinking is not
// included: copy reproduces what the model said, not how it got there.
func copyMessageText(msg *message.Message) string {
	var parts []string
	for _, c := range message.PartsOf[message.TextContent](msg) {
		if strings.TrimSpace(c.Text) != "" {
			parts = append(parts, strings.TrimRight(c.Text, "\n"))
		}
	}
	return strings.Join(parts, "\n\n")
}

// copyAttachments lists a message's attachments by name and type.
// The bytes themselves cannot go on a text clipboard, so naming them
// is the honest alternative to dropping them without a word.
func copyAttachments(msg *message.Message) string {
	binary := msg.BinaryContent()
	urls := msg.ImageURLContent()
	if len(binary) == 0 && len(urls) == 0 {
		return ""
	}
	lines := []string{"**Attachments:**"}
	for _, at := range binary {
		name := at.Path
		if name == "" {
			name = "(unnamed)"
		}
		lines = append(lines, fmt.Sprintf("- %s (%s)", name, at.MIMEType))
	}
	for _, at := range urls {
		lines = append(lines, "- "+at.URL)
	}
	return strings.Join(lines, "\n")
}

// -----------------------------------------------------------------------------
// Tool calls
// -----------------------------------------------------------------------------

// copyTitle names a tool call in a heading. It reads the same label
// table the transcript renders, so what a paste says matches what was
// on screen, including the lsp tool's per-action names.
func copyTitle(tc message.ToolCall) string {
	return ToolDisplayName(tc)
}

// formatToolForCopy formats a standalone tool call for the clipboard.
func (t *baseToolMessageItem) formatToolForCopy() string {
	return t.copyText(copyBaseDepth)
}

// copyText formats the tool call with its heading at the given depth.
// The body is identical whether the call is collapsed or expanded.
func (t *baseToolMessageItem) copyText(depth int) string {
	sections := []string{copyHeading(depth, copyTitle(t.toolCall)+" Tool Call")}

	if t.toolCall.Input != "" {
		if params := t.formatParametersForCopy(); params != "" {
			sections = append(sections, copyHeading(depth+1, "Parameters:"), params)
		}
	}

	switch {
	case t.result != nil && t.result.ToolCallID != "" && t.result.IsError:
		// Errors get the same fenced treatment as results: an error
		// body is output too, and often the part worth pasting.
		sections = append(sections, copyHeading(depth+1, "Error:"), copyFence("", t.result.Content))
	case t.result != nil && t.result.ToolCallID != "":
		if content := t.formatResultForCopy(); content != "" {
			sections = append(sections, copyHeading(depth+1, "Result:"), content)
		}
	case t.status == ToolStatusCanceled:
		sections = append(sections, copyHeading(depth+1, "Status:"), "Cancelled")
	default:
		sections = append(sections, copyHeading(depth+1, "Status:"), "Pending...")
	}

	return copyJoin(sections...)
}

// formatParametersForCopy formats tool parameters for the clipboard.
func (t *baseToolMessageItem) formatParametersForCopy() string {
	input := t.toolCall.Input
	switch t.toolCall.Name {
	case tools.ShellToolName:
		var params tools.ShellParams
		if json.Unmarshal([]byte(input), &params) == nil {
			// The command is fenced rather than flattened onto one
			// line: a copied heredoc or multi-line pipeline should
			// paste back into a shell and run.
			return copyJoin("**Command:**", copyFence("bash", params.Command))
		}
	case tools.ViewToolName:
		var params tools.ViewParams
		if json.Unmarshal([]byte(input), &params) == nil {
			lines := []string{copyField("File", fsext.PrettyPath(params.FilePath))}
			if params.Offset > 0 {
				lines = append(lines, copyField("Offset", params.Offset))
			}
			if params.Limit > 0 {
				lines = append(lines, copyField("Limit", params.Limit))
			}
			return strings.Join(lines, "\n")
		}
	case tools.EditToolName:
		var params tools.EditParams
		if json.Unmarshal([]byte(input), &params) == nil {
			// The old and new strings are not echoed here: the result
			// carries the same change as a diff, which reads better
			// and does not duplicate the file's text twice over.
			return strings.Join([]string{
				copyField("File", fsext.PrettyPath(params.FilePath)),
				copyField("Edits", len(params.Edits)),
			}, "\n")
		}
	case tools.WriteToolName:
		var params tools.WriteParams
		if json.Unmarshal([]byte(input), &params) == nil {
			return copyField("File", fsext.PrettyPath(params.FilePath))
		}
	case tools.FetchToolName:
		var params tools.FetchParams
		if json.Unmarshal([]byte(input), &params) == nil {
			lines := []string{copyField("URL", params.URL)}
			if params.Format != "" {
				lines = append(lines, copyField("Format", params.Format))
			}
			if params.Timeout > 0 {
				lines = append(lines, copyField("Timeout", fmt.Sprintf("%ds", params.Timeout)))
			}
			return strings.Join(lines, "\n")
		}
	case tools.ResearchToolName:
		var params tools.ResearchParams
		if json.Unmarshal([]byte(input), &params) == nil {
			var lines []string
			if params.URL != "" {
				lines = append(lines, copyField("URL", params.URL))
			}
			if params.Prompt != "" {
				lines = append(lines, "**Prompt:**", params.Prompt)
			}
			return strings.Join(lines, "\n")
		}
	case tools.DiagnosticsToolName:
		return copyField("Project", "diagnostics")
	case agent.AgentToolName:
		var params agent.AgentParams
		if json.Unmarshal([]byte(input), &params) == nil {
			var lines []string
			if params.SubagentType != "" && params.SubagentType != config.AgentTask {
				lines = append(lines, copyField("Subagent", params.SubagentType))
			}
			lines = append(lines, "**Task:**", params.Prompt)
			return strings.Join(lines, "\n")
		}
	}

	// Everything else - MCP servers and any tool without a hand-written
	// shape - gets the raw input as sorted, indented JSON.
	return copyJSON(input)
}

// formatResultForCopy formats a tool result for the clipboard.
func (t *baseToolMessageItem) formatResultForCopy() string {
	if t.result == nil {
		return ""
	}

	if t.result.Data != "" {
		if strings.HasPrefix(t.result.MIMEType, "image/") {
			return fmt.Sprintf("[Image: %s]", t.result.MIMEType)
		}
		return fmt.Sprintf("[Media: %s]", t.result.MIMEType)
	}

	switch t.toolCall.Name {
	case tools.ShellToolName:
		return t.formatShellResultForCopy()
	case tools.ViewToolName:
		return t.formatViewResultForCopy()
	case tools.EditToolName:
		return t.formatEditResultForCopy()
	case tools.WriteToolName:
		return t.formatWriteResultForCopy()
	case tools.FetchToolName:
		return t.formatFetchResultForCopy()
	case tools.ResearchToolName:
		return copyFence("markdown", t.result.Content)
	case agent.AgentToolName:
		return copyFence("markdown", t.result.Content)
	case tools.DiagnosticsToolName:
		return copyFence("", t.result.Content)
	}

	if strings.HasPrefix(t.toolCall.Name, "mcp_") {
		return copyJSON(t.result.Content)
	}
	// An unrecognized tool's result is opaque text; fencing it is what
	// keeps a pasted transcript from reflowing it into a paragraph.
	return copyFence("", t.result.Content)
}

// formatShellResultForCopy fences a command's output and, when the run
// was timed, notes how long it took. There is no exit code line: the
// tool records the output and the timing, not the status.
func (t *baseToolMessageItem) formatShellResultForCopy() string {
	var meta tools.ShellResponseMetadata
	if t.result.Metadata != "" {
		json.Unmarshal([]byte(t.result.Metadata), &meta)
	}

	output := meta.Output
	if output == "" && t.result.Content != tools.ShellNoOutput {
		output = t.result.Content
	}

	var parts []string
	if d := shellCopyDuration(meta); d != "" {
		parts = append(parts, copyField("Duration", d))
	}
	if output == "" {
		parts = append(parts, "(no output)")
	} else {
		parts = append(parts, copyFence("bash", output))
	}
	return copyJoin(parts...)
}

// shellCopyDuration renders the wall time of a shell run, or "" when
// the metadata did not carry a usable window.
func shellCopyDuration(meta tools.ShellResponseMetadata) string {
	if meta.StartTime <= 0 || meta.EndTime <= meta.StartTime {
		return ""
	}
	d := time.Duration(meta.EndTime-meta.StartTime) * time.Millisecond
	return d.Round(time.Millisecond).String()
}

// formatViewResultForCopy fences a viewed file with the language its
// extension implies.
func (t *baseToolMessageItem) formatViewResultForCopy() string {
	var meta tools.ViewResponseMetadata
	if t.result.Metadata != "" {
		json.Unmarshal([]byte(t.result.Metadata), &meta)
	}
	if meta.Content == "" {
		return copyFence("", t.result.Content)
	}
	return copyFence(copyFenceLang(meta.FilePath), meta.Content)
}

// formatEditResultForCopy renders the change as a unified diff.
func (t *baseToolMessageItem) formatEditResultForCopy() string {
	var meta tools.EditResponseMetadata
	if t.result.Metadata == "" || json.Unmarshal([]byte(t.result.Metadata), &meta) != nil {
		return copyFence("", t.result.Content)
	}
	if meta.OldContent == "" && meta.NewContent == "" {
		return copyFence("", t.result.Content)
	}

	var params tools.EditParams
	json.Unmarshal([]byte(t.toolCall.Input), &params)
	fileName := params.FilePath
	if fileName != "" {
		fileName = fsext.PrettyPath(fileName)
	}

	diffContent, additions, removals := diff.GenerateDiff(meta.OldContent, meta.NewContent, fileName)
	return copyJoin(
		fmt.Sprintf("Changes: +%d -%d", additions, removals),
		copyFence("diff", diffContent),
	)
}

// formatWriteResultForCopy reproduces the file that was written. The
// text comes from the call's input rather than the result, which only
// reports that the write happened - copying a write and getting back
// "File written" instead of the file would be useless.
func (t *baseToolMessageItem) formatWriteResultForCopy() string {
	var params tools.WriteParams
	if json.Unmarshal([]byte(t.toolCall.Input), &params) != nil {
		return copyFence("", t.result.Content)
	}
	return copyFence(copyFenceLang(params.FilePath), params.Content)
}

// formatFetchResultForCopy fences a fetched document in the format it
// was requested in. The URL is not repeated here - it is already in the
// Parameters section directly above.
func (t *baseToolMessageItem) formatFetchResultForCopy() string {
	var params tools.FetchParams
	json.Unmarshal([]byte(t.toolCall.Input), &params)
	lang := ""
	switch strings.ToLower(params.Format) {
	case "markdown", "md":
		lang = "markdown"
	case "html":
		lang = "html"
	}
	return copyFence(lang, t.result.Content)
}

// -----------------------------------------------------------------------------
// Tool groups
// -----------------------------------------------------------------------------

// formatGroupForCopy formats a folded run of tool calls. With the
// sub-cursor on a child, the child alone is copied: the cursor is the
// selection, and copying four calls when one is highlighted contradicts
// what the screen shows. Otherwise the whole run is copied under a
// header naming it, with the children demoted one heading level so the
// document nests properly.
func (g *ToolGroupMessageItem) formatGroupForCopy() string {
	// The sub-cursor only means anything while the group holds focus,
	// which is the same condition the render path bars on.
	if child := g.selectedChild; g.focused && child >= 0 && child < len(g.tools) {
		return toolCopyText(g.tools[child], copyBaseDepth)
	}

	sections := []string{copyHeading(copyBaseDepth, fmt.Sprintf("%s (%d tool calls)", g.groupVerb(), len(g.tools)))}
	for _, t := range g.tools {
		sections = append(sections, toolCopyText(t, copyBaseDepth+1))
	}
	return copyJoin(sections...)
}

// toolCopyText formats one tool item at the given heading depth,
// tolerating an item that does not implement the copy hook.
func toolCopyText(t ToolMessageItem, depth int) string {
	if c, ok := t.(interface{ copyText(int) string }); ok {
		return c.copyText(depth)
	}
	return ""
}

// -----------------------------------------------------------------------------
// Plain items
// -----------------------------------------------------------------------------

// copyPlainRender is the copy text for items whose whole content is the
// line they render: the assistant footer, the subagent wait entry. The
// rendered form is the content, so it is copied with styling stripped
// rather than left uncopyable.
func copyPlainRender(item interface{ RawRender(int) string }, width int) string {
	return strings.TrimRight(ansi.Strip(item.RawRender(width)), " \n")
}
