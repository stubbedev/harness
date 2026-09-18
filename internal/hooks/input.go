package hooks

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/stubbedev/harness/internal/shell"
	"github.com/tidwall/gjson"
)

// SupportedOutputVersion is the highest envelope version this build
// understands. Hooks may omit `version` entirely (treated as 1) or pin
// an older version. Unknown higher versions are still parsed but logged.
const SupportedOutputVersion = 1

// Payload is the JSON structure piped to hook commands via stdin.
// ToolInput is emitted as a parsed JSON object for compatibility with
// Claude Code hooks (which expect tool_input to be an object, not a
// string). Fields beyond the common envelope are event-specific and
// omitted when empty.
type Payload struct {
	Event            string          `json:"event"`
	SessionID        string          `json:"session_id"`
	CWD              string          `json:"cwd"`
	ToolName         string          `json:"tool_name,omitempty"`
	ToolInput        json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse     *ToolResponse   `json:"tool_response,omitempty"`
	Prompt           string          `json:"prompt,omitempty"`
	Attachments      []string        `json:"attachments,omitempty"`
	SubagentType     string          `json:"subagent_type,omitempty"`
	Trigger          string          `json:"trigger,omitempty"`
	NotificationType string          `json:"notification_type,omitempty"`
	Message          string          `json:"message,omitempty"`
}

// ToolResponse describes the completed tool call included in
// PostToolUse payloads.
type ToolResponse struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// EventContext carries everything needed to fire one hook event: the
// payload fields plus the working directory used for env vars. Zero
// fields are omitted from the payload.
type EventContext struct {
	Event            string
	SessionID        string
	CWD              string
	ToolName         string
	ToolInput        string // Raw JSON string as the model sent it.
	ToolResponse     *ToolResponse
	Prompt           string
	Attachments      []string
	SubagentType     string
	Trigger          string
	NotificationType string
	Message          string
}

// Subject returns the string a hook matcher is tested against: the tool
// name for tool events, the sub-agent type for SubagentStop, and "" for
// events without a natural subject (an empty matcher matches those).
func (ec EventContext) Subject() string {
	switch ec.Event {
	case EventSubagentStop:
		return ec.SubagentType
	default:
		return ec.ToolName
	}
}

func (ec EventContext) payload() Payload {
	// json.RawMessage is a slice, so omitempty drops only a nil value:
	// non-tool events leave ToolInput unset and the key disappears.
	var toolInput json.RawMessage
	if ec.ToolInput != "" {
		toolInput = json.RawMessage(ec.ToolInput)
		if !json.Valid(toolInput) {
			toolInput = json.RawMessage("{}")
		}
	}
	return Payload{
		Event:     ec.Event,
		SessionID: ec.SessionID,
		// The JSON payload is cross-platform data parsed by hook
		// scripts, so the path always uses forward slashes. The env
		// vars keep the platform-native form for shells.
		CWD:              filepath.ToSlash(ec.CWD),
		ToolName:         ec.ToolName,
		ToolInput:        toolInput,
		ToolResponse:     ec.ToolResponse,
		Prompt:           ec.Prompt,
		Attachments:      ec.Attachments,
		SubagentType:     ec.SubagentType,
		Trigger:          ec.Trigger,
		NotificationType: ec.NotificationType,
		Message:          ec.Message,
	}
}

// BuildEventPayload constructs the JSON stdin payload for an event.
func BuildEventPayload(ec EventContext) []byte {
	data, err := json.Marshal(ec.payload())
	if err != nil {
		return []byte("{}")
	}
	return data
}

// BuildPayload constructs the JSON stdin payload for a hook command.
// Prefer BuildEventPayload for events that carry more than tool fields.
func BuildPayload(eventName, sessionID, cwd, toolName, toolInputJSON string) []byte {
	return BuildEventPayload(EventContext{
		Event:     eventName,
		SessionID: sessionID,
		CWD:       cwd,
		ToolName:  toolName,
		ToolInput: toolInputJSON,
	})
}

// BuildEnv constructs the environment variable slice for a hook command.
// It includes all current process env vars plus hook-specific ones.
func BuildEnv(eventName, toolName, sessionID, cwd, projectDir, toolInputJSON string) []string {
	return BuildEventEnv(EventContext{
		Event:     eventName,
		SessionID: sessionID,
		CWD:       cwd,
		ToolName:  toolName,
		ToolInput: toolInputJSON,
	}, projectDir)
}

// BuildEventEnv constructs the environment for an event, including the
// event-specific variables (prompt, trigger, notification) that the
// legacy BuildEnv wrapper cannot express.
func BuildEventEnv(ec EventContext, projectDir string) []string {
	env := os.Environ()
	env = append(env, shell.HarnessEnvMarkers()...)
	env = append(
		env,
		fmt.Sprintf("HARNESS_EVENT=%s", ec.Event),
		fmt.Sprintf("HARNESS_TOOL_NAME=%s", ec.ToolName),
		fmt.Sprintf("HARNESS_SESSION_ID=%s", ec.SessionID),
		fmt.Sprintf("HARNESS_CWD=%s", ec.CWD),
		fmt.Sprintf("HARNESS_PROJECT_DIR=%s", projectDir),
	)
	if ec.SubagentType != "" {
		env = append(env, fmt.Sprintf("HARNESS_SUBAGENT_TYPE=%s", ec.SubagentType))
	}
	if ec.Prompt != "" {
		env = append(env, fmt.Sprintf("HARNESS_PROMPT=%s", ec.Prompt))
	}
	if ec.Trigger != "" {
		env = append(env, fmt.Sprintf("HARNESS_TRIGGER=%s", ec.Trigger))
	}
	if ec.NotificationType != "" {
		env = append(env, fmt.Sprintf("HARNESS_NOTIFICATION_TYPE=%s", ec.NotificationType))
	}
	if ec.Message != "" {
		env = append(env, fmt.Sprintf("HARNESS_MESSAGE=%s", ec.Message))
	}

	// Extract tool-specific env vars from the JSON input.
	if ec.ToolInput != "" {
		if cmd := gjson.Get(ec.ToolInput, "command"); cmd.Exists() {
			env = append(env, fmt.Sprintf("HARNESS_TOOL_INPUT_COMMAND=%s", cmd.String()))
		}
		if fp := gjson.Get(ec.ToolInput, "file_path"); fp.Exists() {
			env = append(env, fmt.Sprintf("HARNESS_TOOL_INPUT_FILE_PATH=%s", fp.String()))
		}
	}

	return env
}

// parseStdout parses the JSON output from a hook command's stdout.
// Supports both Harness format and Claude Code format (hookSpecificOutput).
func parseStdout(stdout string) HookResult {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return HookResult{Decision: DecisionNone}
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	// Claude Code compat: if hookSpecificOutput is present, parse that.
	if hso, ok := raw["hookSpecificOutput"]; ok {
		return parseClaudeCodeOutput(hso)
	}

	var parsed struct {
		Version       int             `json:"version"`
		Decision      string          `json:"decision"`
		Halt          bool            `json:"halt"`
		Reason        string          `json:"reason"`
		Context       json.RawMessage `json:"context"`
		UpdatedInput  json.RawMessage `json:"updated_input"`
		UpdatedPrompt json.RawMessage `json:"updated_prompt"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	if parsed.Version > SupportedOutputVersion {
		slog.Debug(
			"Hook output declared a newer envelope version than this build supports",
			"version", parsed.Version,
			"supported", SupportedOutputVersion,
		)
	}

	result := HookResult{
		Halt:          parsed.Halt,
		Reason:        parsed.Reason,
		Context:       parseContext(parsed.Context),
		UpdatedInput:  rawToString(parsed.UpdatedInput),
		UpdatedPrompt: rawToString(parsed.UpdatedPrompt),
	}
	result.Decision = ParseDecision(parsed.Decision)
	return result
}

// parseContext accepts either a single string or an array of strings and
// returns a newline-joined value with empty entries dropped.
func parseContext(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// String form.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return ""
	}
	// Array form.
	if raw[0] == '[' {
		var items []string
		if err := json.Unmarshal(raw, &items); err != nil {
			return ""
		}
		out := items[:0]
		for _, s := range items {
			if s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// parseClaudeCodeOutput handles the Claude Code hook output format:
// {"hookSpecificOutput": {"permissionDecision": "allow", ...}}
func parseClaudeCodeOutput(data json.RawMessage) HookResult {
	var hso struct {
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
		AdditionalContext        string          `json:"additionalContext"`
	}
	if err := json.Unmarshal(data, &hso); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	result := HookResult{
		Decision: ParseDecision(hso.PermissionDecision),
		Reason:   hso.PermissionDecisionReason,
		Context:  hso.AdditionalContext,
	}

	// Marshal updatedInput back to a string for our opaque format.
	if len(hso.UpdatedInput) > 0 && string(hso.UpdatedInput) != "null" {
		result.UpdatedInput = string(hso.UpdatedInput)
	}

	return result
}

// rawToString converts a json.RawMessage to a string suitable for use
// as opaque tool input. It accepts both a JSON object (nested) and a
// JSON string (stringified, for backward compatibility).
func rawToString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// If it's a JSON string, unwrap it.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	// Otherwise it's an object/array — use as-is.
	return string(raw)
}

// ParseDecision maps a hook-authored decision string onto a Decision.
// Matching is case-insensitive; "deny" and "block" both deny, "allow"
// allows, and anything else expresses no opinion. The vocabulary is
// shared by shell-hook stdout, the Claude Code format, and Lua handlers
// so the same verdict string means the same thing on every path.
func ParseDecision(s string) Decision {
	switch strings.ToLower(s) {
	case "allow":
		return DecisionAllow
	case "deny", "block":
		return DecisionDeny
	default:
		return DecisionNone
	}
}
