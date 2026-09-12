package model

import (
	"strconv"
	"strings"

	"github.com/stubbedev/harness/internal/ui/completions"
	"github.com/stubbedev/harness/internal/workspace"
)

// buildSubagentCaches projects the workspace's active subagents into the two
// shapes the UI consumes: completion items (for the @-mention picker) and a
// name set (for sendMessage rewriting). Iteration order matches the input so
// completion ordering is deterministic.
func buildSubagentCaches(active []workspace.SubagentInfo) ([]completions.SubagentCompletionValue, map[string]bool) {
	items := make([]completions.SubagentCompletionValue, len(active))
	names := make(map[string]bool, len(active))
	for i, sa := range active {
		items[i] = completions.SubagentCompletionValue{Name: sa.Name, Description: sa.Description}
		names[sa.Name] = true
	}
	return items, names
}

// rebuildSubagentCaches refreshes the @-mention completion caches from the
// workspace's current active subagents. Called when Library discovery changes.
func (m *UI) rebuildSubagentCaches() {
	m.activeSubagentItems, m.activeSubagentNames = buildSubagentCaches(m.com.Workspace.ActiveSubagents())
}

// leadingSubagentMentions consumes the run of `@name` tokens at the start of
// content — each followed by whitespace (space, tab, or newline) — for as long
// as every name is a known active subagent. It returns the names in the order
// written and the remaining text. Scanning stops at the first token that is
// not an `@mention` of an active subagent, so `@known @unknown do X` yields
// one name and leaves "@unknown do X" as the prompt.
func leadingSubagentMentions(content string, activeNames map[string]bool) (names []string, rest string) {
	rest = content
	for strings.HasPrefix(rest, "@") {
		after := rest[1:]
		idx := strings.IndexAny(after, " \t\n\r")
		if idx < 0 {
			break
		}
		name := after[:idx]
		if !activeNames[name] {
			break
		}
		names = append(names, name)
		rest = strings.TrimLeft(after[idx+1:], " \t\n\r")
	}
	return names, rest
}

// rewriteSubagentPrompt detects one or more `@name` mentions at the start of
// content and rewrites them to a delegation instruction. A single mention
// delegates to that subagent; several fan out, dispatching all of them in one
// message so they run concurrently. Returns content unchanged when no leading
// mention names an active subagent, or when nothing follows the mentions.
func rewriteSubagentPrompt(content string, activeNames map[string]bool) string {
	names, prompt := leadingSubagentMentions(content, activeNames)
	prompt = strings.TrimSpace(prompt)
	if len(names) == 0 || prompt == "" {
		return content
	}
	if len(names) == 1 {
		return `Use the agent tool with subagent_type="` + names[0] + `" to handle this request: ` + prompt
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = `"` + n + `"`
	}
	// One message with every call in it, stated explicitly: the user named
	// several agents because they want them working at the same time, and the
	// dispatcher tool only runs them concurrently when the calls share a
	// message.
	return "Handle this request by dispatching to every one of these subagents: " +
		strings.Join(quoted, ", ") +
		". Issue all " + strconv.Itoa(len(names)) + " agent tool calls in a single message so they run concurrently, " +
		"giving each a self-contained prompt for its part of the work, then combine their results. " +
		"The request: " + prompt
}
