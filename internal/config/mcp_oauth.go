package config

// UsesOAuthFlow reports whether the server authenticates through the
// interactive OAuth flow: OAuth enabled on a remote (HTTP or SSE)
// transport. Both remote transports carry an OAuth handler, so both can
// sit in the needs-auth state and be authenticated from the UI.
func (m MCPConfig) UsesOAuthFlow() bool {
	return m.OAuth && (m.Type == MCPHttp || m.Type == MCPSSE)
}
