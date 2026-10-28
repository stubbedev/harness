package chat

import (
	"encoding/json"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/message"
)

// backgroundShellCall builds the input of a shell call that starts a
// background job.
const backgroundShellCall = `{"command":"go test ./...","run_in_background":true}`

// backgroundShellResult is the result a background shell call returns:
// the job's start acknowledgement and the metadata naming the job.
func backgroundShellResult(t *testing.T, jobID string) *message.ToolResult {
	t.Helper()

	meta, err := json.Marshal(tools.ShellResponseMetadata{
		Background: true,
		JobID:      jobID,
		Outcome:    tools.ShellOutcomeRunning,
	})
	require.NoError(t, err)
	return &message.ToolResult{
		ToolCallID: "t1",
		Name:       "shell",
		Content: "[background job " + jobID + " started; it keeps running on its own]\n" +
			"Its output and exit code arrive through the wait tool, with no polling.\n" +
			"Full log: /tmp/" + jobID + ".log",
		Metadata: string(meta),
	}
}

// TestBackgroundShellNamesTheJob pins that a background call reads as a
// job, not as a terminal session, and that its start acknowledgement is
// summarised by the header rather than repeated under it.
func TestBackgroundShellNamesTheJob(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	item := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "shell", Input: backgroundShellCall, Finished: true,
	}, backgroundShellResult(t, "job-1"), false)

	plain := ansi.Strip(item.Render(80))
	require.Contains(t, plain, "Job job-1")
	require.Contains(t, plain, "go test ./...")
	require.NotContains(t, plain, "Shell")
	require.NotContains(t, plain, "keeps running on its own", "the header already says the call started a job")
	require.NotContains(t, plain, "Full log")

	item.SetExpansionLevel(1)
	expanded := ansi.Strip(item.Render(80))
	require.Contains(t, expanded, "Job job-1")
	require.Contains(t, expanded, "keeps running on its own")
	require.Contains(t, expanded, "Full log: /tmp/job-1.log")
}

// TestPendingBackgroundShellNamesTheJob covers a call whose input has
// arrived but whose result has not: the header still names it as a job,
// so it never paints as a session command and then turns into one.
func TestPendingBackgroundShellNamesTheJob(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	item := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "shell", Input: backgroundShellCall,
	}, nil, false)
	require.Equal(t, ToolStatusRunning, item.EffectiveStatus())

	plain := ansi.Strip(item.Render(80))
	require.Contains(t, plain, "Job")
	require.Contains(t, plain, "go test ./...")
	require.NotContains(t, plain, "Shell")
	require.Contains(t, plain, "Waiting for tool response")
}

// TestBackgroundShellStreamsTheCommand pins that a still-streaming
// background call shows the command as the model types it.
func TestBackgroundShellStreamsTheCommand(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	item := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID:    "t1",
		Name:  "shell",
		Input: `{"command":"go test ./internal/ui","run_in_background":true,"session":`,
	}, nil, false)

	plain := ansi.Strip(item.Render(80))
	require.Contains(t, plain, "Job go test ./internal/ui")
	require.NotContains(t, plain, "Shell")
}
