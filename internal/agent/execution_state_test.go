package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

func executionMessages(id, name, input, metadata string, failed bool) []message.Message {
	return []message.Message{
		{ID: id + "-call", Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: id, Name: name, Input: input, Finished: true}}},
		{ID: id + "-result", Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: id, Name: name, Content: "result", Metadata: metadata, IsError: failed}}},
	}
}

func TestExecutionStateLegacyAndEmpty(t *testing.T) {
	t.Parallel()
	for _, narrative := range []string{"", "old summary", `{"harness_execution_version":2,"narrative":"future"}`} {
		state := newExecutionState(narrative)
		require.Empty(t, state.Render())
		require.Equal(t, narrative, state.Summary(narrative))
		require.Equal(t, narrative, renderExecutionSummary(narrative))
	}
	state := newExecutionState("")
	state.Ingest(executionMessages("read", "view", `{"file_path":"/a"}`, "", false))
	require.Empty(t, state.Render())
	require.Equal(t, "plain summary", state.Summary("plain summary"))
}

func TestExecutionStateFailuresCanonicalRetry(t *testing.T) {
	t.Parallel()
	state := newExecutionState("")
	failed := executionMessages("one", "edit", `{"file_path":"/a","edits":[]}`, "", true)
	state.Ingest(failed)
	require.Len(t, state.Failures, 1)
	snapshot := state.Render()
	state.Ingest(failed)
	require.Equal(t, snapshot, state.Render())
	state = newExecutionState(state.Summary("no useful facts"))
	state.Ingest(executionMessages("two", "edit", `{ "edits": [], "file_path": "/a" }`, "", false))
	require.Empty(t, state.Failures)
}

func TestExecutionStateShellOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, metadata, status string
		failure                bool
	}{
		{"nonzero", `{"exit_code":1}`, "failed", true},
		{"zero", `{"exit_code":0}`, "succeeded", false},
		{"running", `{"running":true}`, "running", false},
		{"waiting", `{"waiting":true}`, "waiting", false},
		{"queued", `{"queued":true}`, "queued", false},
		{"legacy", `{}`, "unknown", false},
		{"interrupted", `{"interrupted":true}`, "interrupted", true},
		{"shell died", `{"shell_exited":true,"shell_exit_code":9}`, "shell_exited", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := newExecutionState("")
			state.Ingest(executionMessages("cmd", "shell", `{"command":"go test ./..."}`, tc.metadata, false))
			require.Len(t, state.Commands, 1)
			require.Equal(t, tc.status, state.Commands[0].Status)
			require.Equal(t, tc.failure, len(state.Failures) > 0)
		})
	}
}

func TestExecutionStatePollSurvivesRestartAndRetry(t *testing.T) {
	t.Parallel()
	state := newExecutionState("")
	state.Ingest(executionMessages("first", "shell", `{"command":"false","session":"test"}`, `{"running":true,"session":"test"}`, false))
	state = newExecutionState(state.Summary("forgot command"))
	state.Ingest(executionMessages("poll", "shell", `{"session":"test"}`, `{"exit_code":2,"session":"test"}`, false))
	require.Len(t, state.Failures, 1)
	require.Equal(t, "false", state.Failures[0].Input)
	state.Ingest(executionMessages("retry", "shell", `{"session":"test","command":"false"}`, `{"exit_code":0,"session":"test"}`, false))
	require.Empty(t, state.Failures)
	require.Equal(t, "succeeded", state.Sessions[0].Status)
}

func TestExecutionStateBoundsAndVersions(t *testing.T) {
	t.Parallel()
	state := newExecutionState("")
	for i := range 100 {
		state.Ingest(executionMessages(fmt.Sprint(i), "write", `{"file_path":"/a"}`, fmt.Sprintf(`{"changed_files":[{"path":"/file/%d","version":"v%d"}]}`, i, i), false))
		state.Ingest(executionMessages(fmt.Sprintf("cmd%d", i), "shell", fmt.Sprintf(`{"command":"echo %d"}`, i), `{"exit_code":0}`, false))
	}
	require.Len(t, state.Files, executionFilesLimit)
	require.Len(t, state.Commands, executionCommandsLimit)
	require.EqualValues(t, 100-executionFilesLimit, state.Omitted["changed_files"])
	require.EqualValues(t, 100-executionCommandsLimit, state.Omitted["commands"])
	snapshot := state.Render()
	state = newExecutionState(state.Summary("nothing happened"))
	require.Equal(t, snapshot, state.Render())
	state.Ingest(executionMessages("new-version", "edit", `{}`, `{"changed_files":[{"path":"/file/99","version":"v100"}]}`, false))
	require.Len(t, state.Files, executionFilesLimit)
	require.Contains(t, string(state.Files[len(state.Files)-1].Metadata), "v100")
}

func TestExecutionStateVerificationMetadata(t *testing.T) {
	t.Parallel()
	meta, _ := json.Marshal(map[string]any{"verification": map[string]any{"status": "failed", "revision_before": "abc", "revision_after": "abc", "checks": []any{map[string]any{"name": "test", "status": "failed", "exit_code": 7, "output": strings.Repeat("huge", 10000)}}}})
	state := newExecutionState("")
	state.Ingest(executionMessages("verify", "verify", `{}`, string(meta), false))
	require.Len(t, state.Verification, 1)
	require.Equal(t, "failed", state.Verification[0].Status)
	require.Contains(t, string(state.Verification[0].Metadata), `"exit_code":7`)
	require.Contains(t, string(state.Verification[0].Metadata), "abc")
	require.NotContains(t, state.Render(), "huge")
}

func TestExecutionStateMetadataBound(t *testing.T) {
	t.Parallel()
	state := newExecutionState("")
	metadata, _ := json.Marshal(map[string]any{"handle": "job1", "output": strings.Repeat("x", 10000)})
	state.Ingest(executionMessages("job", "agent", `{}`, string(metadata), false))
	require.Len(t, state.Jobs, 1)
	require.Equal(t, "job1", state.Jobs[0].Key)
	require.LessOrEqual(t, len(state.Jobs[0].Metadata), executionMetadataLimit)
	require.Contains(t, string(state.Jobs[0].Metadata), "omitted_bytes")
}

func TestExecutionStateTotalBudget(t *testing.T) {
	t.Parallel()
	state := newExecutionState("")
	for i := range 100 {
		metadata, _ := json.Marshal(map[string]any{"changed_files": []any{map[string]any{"path": fmt.Sprintf("/file/%d", i), "version": strings.Repeat("v", 1500)}}})
		state.Ingest(executionMessages(fmt.Sprint(i), "write", `{}`, string(metadata), false))
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.LessOrEqual(t, len(data), 32768)
	require.Positive(t, state.Omitted["changed_files"])
	snapshot := state.Render()
	require.Equal(t, snapshot, newExecutionState(state.Summary("summary")).Render())
}

func TestExecutionStatePartialEditAndVerificationRetry(t *testing.T) {
	t.Parallel()
	state := newExecutionState("")
	state.Ingest(executionMessages("edit", "edit", `{"file_path":"/a"}`, `{"edits_applied":1,"edits_failed":[{"error":"missing"}]}`, false))
	require.Len(t, state.Files, 1)
	require.Contains(t, string(state.Files[0].Metadata), "version_unavailable")
	require.Len(t, state.Failures, 1)
	state.Ingest(executionMessages("verify1", "verify", `{}`, `{"verification":{"status":"failed"}}`, false))
	require.Len(t, state.Failures, 2)
	state.Ingest(executionMessages("verify2", "verify", `{}`, `{"verification":{"status":"skipped"}}`, false))
	require.Len(t, state.Failures, 2)
	state.Ingest(executionMessages("verify3", "verify", `{}`, `{"verification":{"status":"passed"}}`, false))
	require.Len(t, state.Failures, 1)
}
