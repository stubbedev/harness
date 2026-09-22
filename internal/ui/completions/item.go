// Package completions is the @-mention source: the value types the
// editor inserts, the async loaders that fill them, and the tiered
// name-priority filter that ranks file matches. The picker surface is
// the shared dialog machinery (see dialog.MentionPicker and
// dialog.PickerItem); this package owns what is picked, not how it
// renders.
package completions

// FileCompletionValue represents a file path mention value.
type FileCompletionValue struct {
	Path string
}

// ResourceCompletionValue represents an MCP resource mention value.
type ResourceCompletionValue struct {
	MCPName  string
	URI      string
	Title    string
	MIMEType string
}

// SubagentCompletionValue represents a subagent @-mention value.
type SubagentCompletionValue struct {
	Name        string
	Description string
}
