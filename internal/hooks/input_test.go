package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

// shQuote single-quotes s for the embedded shell. Test paths come from
// t.TempDir and never contain quotes.
func shQuote(s string) string {
	return "'" + s + "'"
}

// TestBuildEventPayloadPerEvent pins the stdin payload shape of every
// event against the Reference section of docs/hooks/README.md: which
// fields each event carries, and — just as important — which fields it
// must not carry.
func TestBuildEventPayloadPerEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ec   EventContext
		want string
	}{
		{
			name: "PreToolUse carries tool fields with tool_input as object",
			ec: EventContext{
				Event:     EventPreToolUse,
				SessionID: "s1",
				CWD:       "/w",
				ToolName:  "shell",
				ToolInput: `{"command":"ls"}`,
			},
			want: `{
				"event": "PreToolUse",
				"session_id": "s1",
				"cwd": "/w",
				"tool_name": "shell",
				"tool_input": {"command": "ls"}
			}`,
		},
		{
			name: "PostToolUse adds the completed tool_response",
			ec: EventContext{
				Event:     EventPostToolUse,
				SessionID: "s1",
				CWD:       "/w",
				ToolName:  "shell",
				ToolInput: `{"command":"ls"}`,
				ToolResponse: &ToolResponse{
					Content: "all tests passed",
					IsError: false,
				},
			},
			want: `{
				"event": "PostToolUse",
				"session_id": "s1",
				"cwd": "/w",
				"tool_name": "shell",
				"tool_input": {"command": "ls"},
				"tool_response": {"content": "all tests passed", "is_error": false}
			}`,
		},
		{
			name: "UserPromptSubmit carries prompt and attachments",
			ec: EventContext{
				Event:       EventUserPromptSubmit,
				SessionID:   "s1",
				CWD:         "/w",
				Prompt:      "fix the login flow",
				Attachments: []string{"screenshot.png"},
			},
			want: `{
				"event": "UserPromptSubmit",
				"session_id": "s1",
				"cwd": "/w",
				"prompt": "fix the login flow",
				"attachments": ["screenshot.png"]
			}`,
		},
		{
			name: "SessionStart carries the prompt",
			ec: EventContext{
				Event:     EventSessionStart,
				SessionID: "s1",
				CWD:       "/w",
				Prompt:    "first",
			},
			want: `{
				"event": "SessionStart",
				"session_id": "s1",
				"cwd": "/w",
				"prompt": "first"
			}`,
		},
		{
			name: "Stop carries only the common fields",
			ec: EventContext{
				Event:     EventStop,
				SessionID: "s1",
				CWD:       "/w",
			},
			want: `{
				"event": "Stop",
				"session_id": "s1",
				"cwd": "/w"
			}`,
		},
		{
			name: "SubagentStop carries type and status",
			ec: EventContext{
				Event:        EventSubagentStop,
				SessionID:    "s1",
				CWD:          "/w",
				SubagentType: "fast",
				Message:      "completed",
			},
			want: `{
				"event": "SubagentStop",
				"session_id": "s1",
				"cwd": "/w",
				"subagent_type": "fast",
				"message": "completed"
			}`,
		},
		{
			name: "Notification carries type and message",
			ec: EventContext{
				Event:            EventNotification,
				SessionID:        "s1",
				CWD:              "/w",
				NotificationType: "agent_retrying",
				Message:          "overloaded; retrying in 1ms (attempt 1)",
			},
			want: `{
				"event": "Notification",
				"session_id": "s1",
				"cwd": "/w",
				"notification_type": "agent_retrying",
				"message": "overloaded; retrying in 1ms (attempt 1)"
			}`,
		},
		{
			name: "Notification drops an empty message key",
			ec: EventContext{
				Event:            EventNotification,
				SessionID:        "s1",
				CWD:              "/w",
				NotificationType: "agent_finished",
			},
			want: `{
				"event": "Notification",
				"session_id": "s1",
				"cwd": "/w",
				"notification_type": "agent_finished"
			}`,
		},
		{
			name: "PreCompact carries the trigger",
			ec: EventContext{
				Event:     EventPreCompact,
				SessionID: "s1",
				CWD:       "/w",
				Trigger:   "manual",
			},
			want: `{
				"event": "PreCompact",
				"session_id": "s1",
				"cwd": "/w",
				"trigger": "manual"
			}`,
		},
		{
			name: "PostCompact carries the trigger",
			ec: EventContext{
				Event:     EventPostCompact,
				SessionID: "s1",
				CWD:       "/w",
				Trigger:   "auto",
			},
			want: `{
				"event": "PostCompact",
				"session_id": "s1",
				"cwd": "/w",
				"trigger": "auto"
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.JSONEq(t, tt.want, string(BuildEventPayload(tt.ec)))
		})
	}
}

// TestBuildEventPayloadToolInputIsObject pins the Claude Code
// compatibility rule: tool_input is piped as a parsed JSON object, not
// as the raw string the model sent.
func TestBuildEventPayloadToolInputIsObject(t *testing.T) {
	t.Parallel()

	payload := BuildEventPayload(EventContext{
		Event:     EventPreToolUse,
		SessionID: "s1",
		ToolName:  "shell",
		ToolInput: `{"command":"ls"}`,
	})

	var doc struct {
		ToolInput json.RawMessage `json:"tool_input"`
	}
	require.NoError(t, json.Unmarshal(payload, &doc))
	require.NotEmpty(t, doc.ToolInput)
	require.Equal(t, "{", strings.TrimSpace(string(doc.ToolInput))[:1],
		"tool_input must arrive as a JSON object, not a string")
}

// envMap parses a KEY=VALUE slice into a map, last occurrence winning.
func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		out[k] = v
	}
	return out
}

// TestBuildEventEnvPerEvent pins the documented environment variables:
// every event sees the common ones, and the event-specific variables
// appear only for the events that carry the corresponding payload field.
func TestBuildEventEnvPerEvent(t *testing.T) {
	t.Parallel()

	common := EventContext{
		Event:     EventStop,
		SessionID: "s1",
		CWD:       "/w",
		ToolName:  "shell",
		ToolInput: `{"command":"ls","file_path":"/tmp/f.txt"}`,
	}

	t.Run("common variables on every event", func(t *testing.T) {
		t.Parallel()
		m := envMap(BuildEventEnv(common, "/project"))
		require.Equal(t, EventStop, m["HARNESS_EVENT"])
		require.Equal(t, "shell", m["HARNESS_TOOL_NAME"])
		require.Equal(t, "s1", m["HARNESS_SESSION_ID"])
		require.Equal(t, "/w", m["HARNESS_CWD"])
		require.Equal(t, "/project", m["HARNESS_PROJECT_DIR"])
		require.Equal(t, "ls", m["HARNESS_TOOL_INPUT_COMMAND"])
		require.Equal(t, "/tmp/f.txt", m["HARNESS_TOOL_INPUT_FILE_PATH"])
	})

	t.Run("event-specific variables are event-gated", func(t *testing.T) {
		t.Parallel()
		m := envMap(BuildEventEnv(common, "/project"))
		for _, key := range []string{
			"HARNESS_PROMPT",
			"HARNESS_SUBAGENT_TYPE",
			"HARNESS_TRIGGER",
			"HARNESS_NOTIFICATION_TYPE",
			"HARNESS_MESSAGE",
		} {
			require.NotContains(t, m, key, "Stop must not export %s", key)
		}
	})

	t.Run("UserPromptSubmit exports the prompt", func(t *testing.T) {
		t.Parallel()
		ec := common
		ec.Event = EventUserPromptSubmit
		ec.Prompt = "fix it"
		m := envMap(BuildEventEnv(ec, "/project"))
		require.Equal(t, "fix it", m["HARNESS_PROMPT"])
	})

	t.Run("SessionStart exports the prompt", func(t *testing.T) {
		t.Parallel()
		ec := common
		ec.Event = EventSessionStart
		ec.Prompt = "first"
		m := envMap(BuildEventEnv(ec, "/project"))
		require.Equal(t, "first", m["HARNESS_PROMPT"])
	})

	t.Run("SubagentStop exports the sub-agent type", func(t *testing.T) {
		t.Parallel()
		ec := common
		ec.Event = EventSubagentStop
		ec.SubagentType = "task"
		m := envMap(BuildEventEnv(ec, "/project"))
		require.Equal(t, "task", m["HARNESS_SUBAGENT_TYPE"])
	})

	t.Run("compact events export the trigger", func(t *testing.T) {
		t.Parallel()
		pre := common
		pre.Event = EventPreCompact
		pre.Trigger = "auto"
		m := envMap(BuildEventEnv(pre, "/project"))
		require.Equal(t, "auto", m["HARNESS_TRIGGER"])

		post := common
		post.Event = EventPostCompact
		post.Trigger = "manual"
		m = envMap(BuildEventEnv(post, "/project"))
		require.Equal(t, "manual", m["HARNESS_TRIGGER"])
	})

	t.Run("Notification exports type and message", func(t *testing.T) {
		t.Parallel()
		ec := common
		ec.Event = EventNotification
		ec.NotificationType = "agent_retrying"
		ec.Message = "overloaded"
		m := envMap(BuildEventEnv(ec, "/project"))
		require.Equal(t, "agent_retrying", m["HARNESS_NOTIFICATION_TYPE"])
		require.Equal(t, "overloaded", m["HARNESS_MESSAGE"])
	})
}

// TestRunEventDeliversPayloadAndEnv runs a real hook command through the
// embedded shell and asserts what the hook process actually receives on
// stdin and in its environment — the end-to-end contract behind
// BuildEventPayload and BuildEventEnv.
func TestRunEventDeliversPayloadAndEnv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	envPath := filepath.Join(dir, "env.txt")

	hook := config.HookConfig{
		Command: fmt.Sprintf(
			"cat > %s; printf '%%s\\n' \"$HARNESS\" \"$AGENT\" \"$AI_AGENT\" \"$HARNESS_EVENT\" \"$HARNESS_TOOL_NAME\" \"$HARNESS_SESSION_ID\" \"$HARNESS_CWD\" \"$HARNESS_PROJECT_DIR\" \"$HARNESS_TOOL_INPUT_COMMAND\" > %s",
			shQuote(payloadPath), shQuote(envPath),
		),
	}
	r := NewRunner([]config.HookConfig{hook}, dir, dir)

	result, err := r.RunEvent(context.Background(), EventContext{
		Event:     EventPostToolUse,
		SessionID: "s1",
		ToolName:  "shell",
		ToolInput: `{"command":"ls"}`,
		ToolResponse: &ToolResponse{
			Content: "ok",
			IsError: false,
		},
	})
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)

	payload, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"event": "PostToolUse",
		"session_id": "s1",
		"cwd": "`+filepath.ToSlash(dir)+`",
		"tool_name": "shell",
		"tool_input": {"command": "ls"},
		"tool_response": {"content": "ok", "is_error": false}
	}`, string(payload))

	env, err := os.ReadFile(envPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(env)), "\n")
	require.Equal(t, []string{
		"1",
		"harness",
		"harness",
		EventPostToolUse,
		"shell",
		"s1",
		dir,
		dir,
		"ls",
	}, lines)
}
