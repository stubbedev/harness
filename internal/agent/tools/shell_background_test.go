package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// errTestStart stands in for a job the registry refuses to start.
var errTestStart = errors.New("this session already has 8 background jobs running")

// requireShellAvailable skips a test that builds the shell tool on a host
// where no shell could be identified: NewShellTool returns nil then.
func requireShellAvailable(t *testing.T) {
	t.Helper()
	if !ShellAvailable() {
		t.Skip("no shell could be identified")
	}
}

// fakeBackgroundJobs is a job starter that records what the tool asked
// for instead of running anything.
type fakeBackgroundJobs struct {
	job   BackgroundJob
	err   error
	calls []backgroundCall
}

type backgroundCall struct {
	sessionID  string
	command    string
	workingDir string
}

func (f *fakeBackgroundJobs) StartBackgroundJob(_ context.Context, sessionID, command, workingDir string) (BackgroundJob, error) {
	f.calls = append(f.calls, backgroundCall{sessionID: sessionID, command: command, workingDir: workingDir})
	return f.job, f.err
}

// RunInBackground starts a job through the starter and answers the call
// at once: the response names the job, its log and where its result
// arrives, and carries the same facts in its metadata.
func TestShellTool_RunInBackgroundStartsAJob(t *testing.T) {
	t.Parallel()
	requireShellAvailable(t)

	dir := t.TempDir()
	starter := &fakeBackgroundJobs{job: BackgroundJob{ID: "job-4", LogPath: "/data/jobs/sess-job-4.log"}}
	tool := NewShellTool(dir, t.Name(), nil, nil, starter)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess")

	resp := runShellTool(t, tool, ctx, ShellParams{
		Command:         "go test ./...",
		WorkingDir:      "sub",
		RunInBackground: true,
	})
	require.False(t, resp.IsError)

	var meta ShellResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Background)
	require.Equal(t, "job-4", meta.JobID)
	require.Equal(t, ShellOutcomeRunning, meta.Outcome)
	require.Empty(t, meta.Session, "no terminal is involved in a background job")

	require.Contains(t, resp.Content, "job-4")
	require.Contains(t, resp.Content, "/data/jobs/sess-job-4.log")
	require.Contains(t, resp.Content, "wait tool")
	require.Contains(t, resp.Content, "no polling")

	require.Len(t, starter.calls, 1)
	require.Equal(t, "sess", starter.calls[0].sessionID)
	require.Equal(t, "go test ./...", starter.calls[0].command)
	require.Equal(t, filepath.Join(dir, "sub"), starter.calls[0].workingDir)
}

// A starter that refuses the job fails the call with its reason, which is
// what the model reads: the cap and a missing shell both arrive this way.
func TestShellTool_RunInBackgroundReportsStartFailure(t *testing.T) {
	t.Parallel()
	requireShellAvailable(t)

	starter := &fakeBackgroundJobs{err: errTestStart}
	tool := NewShellTool(t.TempDir(), t.Name(), nil, nil, starter)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess")

	resp := runShellTool(t, tool, ctx, ShellParams{Command: "make", RunInBackground: true})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, errTestStart.Error())
}

// Without a starter the option is advertised by the schema but refused
// with a message rather than silently typing into a terminal.
func TestShellTool_RunInBackgroundNeedsAStarter(t *testing.T) {
	t.Parallel()
	requireShellAvailable(t)

	tool := NewShellTool(t.TempDir(), t.Name(), nil, nil, nil)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess")

	resp := runShellTool(t, tool, ctx, ShellParams{Command: "make", RunInBackground: true})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "background jobs are unavailable")
}

// The background call is a call of its own: it takes no terminal, no
// reset and nothing to wait for, and it needs a command.
func TestShellTool_RunInBackgroundRejectsCombinedCall(t *testing.T) {
	t.Parallel()
	requireShellAvailable(t)

	starter := &fakeBackgroundJobs{job: BackgroundJob{ID: "job-1"}}
	tool := NewShellTool(t.TempDir(), t.Name(), nil, nil, starter)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess")

	for _, params := range []ShellParams{
		{Command: "make", RunInBackground: true, Session: "main"},
		{Command: "make", RunInBackground: true, Reset: true},
		{RunInBackground: true},
		{Command: "make", RunInBackground: true, AutoBackgroundAfter: 5},
	} {
		resp := runShellTool(t, tool, ctx, params)
		require.True(t, resp.IsError, "%+v must be refused", params)
	}
	require.Empty(t, starter.calls, "a refused call starts nothing")
}
