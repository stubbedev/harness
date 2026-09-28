package config

import "strings"

// Config fields are written through sjson paths, where '.' separates keys
// and '*' and '?' are wildcards. A provider or MCP server ID is a map key
// the user chose, so it may hold any of them; spliced into a path unescaped,
// "my.llm" names providers.my.llm and the write lands somewhere else. These
// builders are the one place such paths are made.

// pathKey escapes key for use as one segment of an sjson/gjson path.
func pathKey(key string) string {
	var b strings.Builder
	for _, r := range key {
		switch r {
		case '.', '*', '?', '\\', '|', '#', '@':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ProviderFieldPath is the config path of field on the provider with id.
func ProviderFieldPath(providerID, field string) string {
	return "providers." + pathKey(providerID) + "." + field
}

// MCPFieldPath is the config path of field on the MCP server name.
func MCPFieldPath(name, field string) string {
	return "mcp." + pathKey(name) + "." + field
}

// RecentModelsPath is the config path of the recent-models list for a
// model type.
func RecentModelsPath(modelType SelectedModelType) string {
	return "recent_models." + pathKey(string(modelType))
}
