// Package toolname holds the wire names of the built-in agent tools and
// the naming scheme for MCP tools. It is a leaf so the config (which
// validates allowlists), the tools (which register under these names) and
// the UI (which routes rendering by them) all spell a name one way.
package toolname

import (
	"slices"
	"strings"
)

// Built-in tools an agent's allowlist may name.
const (
	Agent       = "agent"
	Shell       = "shell"
	Harness     = "harness"
	Edit        = "edit"
	LSP         = "lsp"
	Fetch       = "fetch"
	Research    = "research"
	Memory      = "memory"
	Question    = "question"
	WebSearch   = "web_search"
	SendMessage = "send_message"
	View        = "view"
	Verify      = "verify"
	Write       = "write"
	MCPResource = "mcp_resource"
)

// Plumbing tools the harness installs on its own terms. They are never
// named in an allowlist.
const (
	SkillSearch   = "skill_search"
	ToolSearch    = "tool_search"
	ExtensionJobs = "extension_jobs"
)

// allowlistable is every name in the first const block, in the order
// config has always listed them.
var allowlistable = []string{
	Agent, Shell, Harness, Edit, LSP, Fetch, Research, Memory, Question,
	WebSearch, SendMessage, View, Verify, Write, MCPResource,
}

// subagentDefaults is what a generic dispatched agent gets. The shell is
// in it: it is how a subagent finds anything at all.
var subagentDefaults = []string{Edit, Fetch, LSP, SendMessage, Shell, View, WebSearch, Write}

// Allowlistable returns every built-in tool name an agent's AllowedTools
// may contain. MCP tools are not included: they are gated by AllowedMCP.
func Allowlistable() []string { return slices.Clone(allowlistable) }

// SubagentDefaults returns the tools a generic dispatched agent gets.
func SubagentDefaults() []string { return slices.Clone(subagentDefaults) }

// IsBuiltin reports whether name belongs to a built-in tool, plumbing
// included. A built-in can share the MCP prefix (mcp_resource), so this is
// what separates the two.
func IsBuiltin(name string) bool {
	return slices.Contains(allowlistable, name) ||
		name == SkillSearch || name == ToolSearch || name == ExtensionJobs
}

const (
	mcpPrefix       = "mcp_"
	mcpSearchSuffix = "_tool_search"
)

// MCP returns the wire name of tool on the named MCP server.
func MCP(server, tool string) string { return mcpPrefix + server + "_" + tool }

// MCPSearch returns the wire name of the search stub standing in for a
// server whose tools are deferred.
func MCPSearch(server string) string { return mcpPrefix + server + mcpSearchSuffix }

// IsMCPSearch reports whether name is a deferred server's search stub.
// server is the recorded server when known; empty falls back to the
// suffix alone, for calls stored before the server was recorded.
func IsMCPSearch(name, server string) bool {
	if server != "" {
		return name == MCPSearch(server)
	}
	return !IsBuiltin(name) && strings.HasPrefix(name, mcpPrefix) && strings.HasSuffix(name, mcpSearchSuffix)
}

// SplitMCP splits an MCP tool's wire name into its server and tool. The
// encoding is ambiguous when a server name holds an underscore, so server
// is the recorded server when known and decides the split exactly. Empty
// falls back to splitting at the first underscore, for calls stored before
// the server was recorded. Built-in names never split.
func SplitMCP(name, server string) (string, string, bool) {
	if IsBuiltin(name) {
		return "", "", false
	}
	if server != "" {
		tool, ok := strings.CutPrefix(name, mcpPrefix+server+"_")
		return server, tool, ok && tool != ""
	}
	rest, ok := strings.CutPrefix(name, mcpPrefix)
	if !ok {
		return "", "", false
	}
	srv, tool, ok := strings.Cut(rest, "_")
	return srv, tool, ok && srv != "" && tool != ""
}
