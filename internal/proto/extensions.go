package proto

// ExtensionCommandInfo describes a slash command a Lua extension
// registered, as exposed to a frontend.
type ExtensionCommandInfo struct {
	ID          string                     `json:"id"`
	Extension   string                     `json:"extension"`
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Arguments   []ExtensionCommandArgument `json:"arguments,omitempty"`
}

// ExtensionCommandArgument is one value an extension command asks for
// before it runs.
type ExtensionCommandArgument struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// RunExtensionCommandRequest is the request body for expanding an
// extension command into the prompt it stands for.
type RunExtensionCommandRequest struct {
	CommandID string            `json:"command_id"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// RunExtensionCommandResponse carries the expanded prompt.
type RunExtensionCommandResponse struct {
	Prompt string `json:"prompt"`
}
