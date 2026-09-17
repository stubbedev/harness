package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

// conversationExportPath returns the file path where the given session's
// transcript is saved. The path is stable per session so repeated exports
// overwrite the previous file instead of accumulating copies.
func conversationExportPath(dataDirectory, sessionID string) string {
	return filepath.Join(dataDirectory, "exports", sessionID+".md")
}

// saveConversationExport renders the session transcript as markdown and
// writes it inside dataDirectory, returning the written path along with the
// rendered markdown so callers can reuse it without reading the file back.
func saveConversationExport(dataDirectory string, sess session.Session, msgs []message.Message) (string, string, error) {
	content, err := renderConversationMarkdown(sess, msgs, time.Now())
	if err != nil {
		return "", "", err
	}
	path := conversationExportPath(dataDirectory, sess.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", fmt.Errorf("failed to create exports directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", "", fmt.Errorf("failed to write conversation: %w", err)
	}
	return path, content, nil
}

// renderConversationMarkdown renders the whole session as a markdown
// document: a metadata header followed by one section per message, with
// tool calls paired to their results.
func renderConversationMarkdown(sess session.Session, msgs []message.Message, exportedAt time.Time) (string, error) {
	if len(msgs) == 0 {
		return "", fmt.Errorf("this session has no messages to export")
	}

	results := toolResultsByCallID(msgs)

	var b strings.Builder
	title := strings.TrimSpace(sess.Title)
	if title == "" {
		title = "Harness Session"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- Session: `%s`\n", sess.ID)
	fmt.Fprintf(&b, "- Exported: %s\n", exportedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Messages: %d\n", len(msgs))

	for _, msg := range msgs {
		// Tool messages only carry results, which are rendered inline with
		// the assistant tool call that produced them.
		if msg.Role == message.Tool {
			continue
		}
		section := renderMessageMarkdown(msg, results)
		if section == "" {
			continue
		}
		b.WriteString("\n---\n\n")
		b.WriteString(section)
	}

	return b.String(), nil
}

// toolResultsByCallID indexes every tool result in the conversation by the
// ID of the tool call it answers.
func toolResultsByCallID(msgs []message.Message) map[string]message.ToolResult {
	results := make(map[string]message.ToolResult)
	for _, msg := range msgs {
		for _, tr := range msg.ToolResults() {
			results[tr.ToolCallID] = tr
		}
	}
	return results
}

// renderMessageMarkdown renders a single message, or an empty string when
// the message has nothing worth exporting.
func renderMessageMarkdown(msg message.Message, results map[string]message.ToolResult) string {
	var body strings.Builder

	for _, sc := range msg.ShellCommands() {
		fmt.Fprintf(&body, "%s\n\n", codeBlock("console", sc.Command))
		if out := strings.TrimSpace(sc.Output); out != "" {
			fmt.Fprintf(&body, "%s\n\n", codeBlock("", out))
		}
		if sc.ExitCode != 0 {
			fmt.Fprintf(&body, "Exit code: %d\n\n", sc.ExitCode)
		}
	}

	if text := strings.TrimSpace(msg.Content().Text); text != "" {
		body.WriteString(text)
		body.WriteString("\n\n")
	}

	for _, img := range msg.ImageURLContent() {
		fmt.Fprintf(&body, "![image](%s)\n\n", img.URL)
	}
	for _, bin := range msg.BinaryContent() {
		name := bin.Path
		if name == "" {
			name = bin.MIMEType
		}
		fmt.Fprintf(&body, "_Attachment: %s (%s)_\n\n", name, bin.MIMEType)
	}

	for _, tc := range msg.ToolCalls() {
		fmt.Fprintf(&body, "#### Tool: %s\n\n", tc.Name)
		if input := strings.TrimSpace(tc.Input); input != "" {
			fmt.Fprintf(&body, "%s\n\n", codeBlock("json", prettyJSON(input)))
		}
		result, ok := results[tc.ID]
		if !ok {
			body.WriteString("_No result recorded._\n\n")
			continue
		}
		label := "Result"
		if result.IsError {
			label = "Error"
		}
		content := strings.TrimSpace(result.Content)
		if content == "" {
			content = "(empty)"
		}
		fmt.Fprintf(&body, "%s\n\n%s\n\n", label+":", codeBlock("", content))
	}

	if finish := msg.FinishPart(); finish != nil && finish.Message != "" {
		fmt.Fprintf(&body, "_%s: %s_\n\n", finish.Reason, finish.Message)
	}

	out := strings.TrimSpace(body.String())
	if out == "" {
		return ""
	}
	return messageHeading(msg) + "\n\n" + out + "\n"
}

// messageHeading builds the "## Role (model) — time" heading for a message.
func messageHeading(msg message.Message) string {
	var heading strings.Builder
	switch msg.Role {
	case message.User:
		heading.WriteString("## User")
	case message.Assistant:
		heading.WriteString("## Assistant")
	case message.System:
		heading.WriteString("## System")
	default:
		heading.WriteString("## ")
		heading.WriteString(string(msg.Role))
	}
	if msg.IsSummaryMessage {
		heading.WriteString(" (summary)")
	}
	if msg.Model != "" {
		fmt.Fprintf(&heading, " · %s", msg.Model)
	}
	if msg.CreatedAt > 0 {
		fmt.Fprintf(&heading, " · %s", time.Unix(msg.CreatedAt, 0).Format(time.RFC3339))
	}
	return heading.String()
}

// codeBlock fences content with a run of backticks long enough to survive
// any backticks inside it.
func codeBlock(lang, content string) string {
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	return fence + lang + "\n" + content + "\n" + fence
}

// prettyJSON indents s when it is valid JSON, and returns it untouched
// otherwise: streamed tool input can be truncated or not JSON at all.
func prettyJSON(s string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return s
	}
	return buf.String()
}
