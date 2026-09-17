package agent

import (
	"fmt"
	"log/slog"
	"regexp"

	"charm.land/fantasy"
)

// maxToolNameLen is the tightest function-name limit among the providers
// harness targets: OpenAI and Gemini cap a tool name at 64 characters,
// Anthropic at 128. One tool list is sent to whichever provider is
// selected, so the shortest bound is the one it has to satisfy.
const maxToolNameLen = 64

// toolNamePattern is the character set providers accept in a function
// name. Anything outside it is rejected before the model ever sees the
// tool.
var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// validateToolNames drops tools whose names would make the provider
// reject the request. The tool list is sent whole, so one unusable name
// is not a degraded tool -- it is a 400 on every turn of the session,
// surfaced as a schema error that names nothing the user can trace back
// to the extension or MCP server behind it.
//
// Built-in names are fixed and covered by tests. These are not: an
// extension's register_tool takes any non-empty string, an MCP tool is
// named "mcp_<server>_<tool>" from a config key and whatever the server
// advertises, and neither is checked at its source. This is the one
// place the model's tool list is assembled, so it is where they are
// checked.
//
// Earlier entries win a collision, which puts built-ins ahead of MCP and
// extension tools and mirrors extensions.Host.Tools, where the second
// extension to claim a name already loses it.
func validateToolNames(list []fantasy.AgentTool) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, tool := range list {
		name := tool.Info().Name
		if problem := toolNameProblem(name, seen); problem != "" {
			slog.Warn("Dropping tool from the model's tool list", "tool", name, "reason", problem)
			continue
		}
		seen[name] = true
		out = append(out, tool)
	}
	return out
}

// toolNameProblem reports why a tool name cannot be advertised, or ""
// when it can.
func toolNameProblem(name string, seen map[string]bool) string {
	switch {
	case !toolNamePattern.MatchString(name):
		return "name must be letters, digits, underscore or hyphen only"
	case len(name) > maxToolNameLen:
		return fmt.Sprintf("name is %d characters, over the %d-character limit", len(name), maxToolNameLen)
	case seen[name]:
		return "name is already taken by an earlier tool"
	}
	return ""
}
