package crash

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// deadPID returns the id of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	require.NoError(t, cmd.Run())
	return cmd.Process.Pid
}

func pendingFile(t *testing.T, dir string, pid int, body string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("%s%d%s", pendingPrefix, pid, pendingSuffix))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	stamp := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, stamp, stamp))
	return path
}

const goTrace = "panic: worker exploded\n\ngoroutine 42 [running]:\nmain.worker()\n\t/x/main.go:12 +0x25\ncreated by main.main in goroutine 1\n"

func TestCollect_TurnsCrashOutputIntoReport(t *testing.T) {
	dir := setupTestDir(t)
	crashedAt := 90 * time.Second
	path := pendingFile(t, dir, deadPID(t), goTrace, crashedAt)

	Collect()

	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "collected output is removed")
	reports, err := List()
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, "fatal", reports[0].Component)
	require.Equal(t, "worker exploded", reports[0].Panic)
	require.WithinDuration(t, time.Now().Add(-crashedAt), reports[0].Time, 2*time.Second, "the report is dated when the process crashed")
	data, err := os.ReadFile(reports[0].Path)
	require.NoError(t, err)
	require.Contains(t, string(data), "main.worker()")
}

func TestCollect_RemovesEmptyFilesOfGoneProcessesOnly(t *testing.T) {
	dir := setupTestDir(t)
	gone := pendingFile(t, dir, deadPID(t), "", time.Hour)
	// The test binary's parent - the go tool - is alive for as long as
	// the test runs.
	alive := pendingFile(t, dir, os.Getppid(), "", time.Hour)

	Collect()

	_, err := os.Stat(gone)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(alive)
	require.NoError(t, err, "a live process's pending file is still in use")
}

func TestCollect_LeavesOutputStillBeingWritten(t *testing.T) {
	dir := setupTestDir(t)
	path := pendingFile(t, dir, os.Getppid(), goTrace, 0)

	Collect()

	_, err := os.Stat(path)
	require.NoError(t, err)
	reports, err := List()
	require.NoError(t, err)
	require.Empty(t, reports)
}

func TestWatchFatal_CleanExitLeavesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "crashes")
	t.Setenv("HARNESS_CRASH_DIR", dir)

	stop := WatchFatal()
	own := filepath.Join(dir, fmt.Sprintf("%s%d%s", pendingPrefix, os.Getpid(), pendingSuffix))
	_, err := os.Stat(own)
	require.NoError(t, err, "the watch has its pending file")
	stop()
	stop()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestFatalSummary(t *testing.T) {
	t.Parallel()

	require.Equal(t, "worker exploded", fatalSummary(goTrace))
	require.Equal(t, "fatal error: concurrent map writes", fatalSummary("\nfatal error: concurrent map writes\n\ngoroutine 1"))
	require.Equal(t, "boom [recovered]", fatalSummary("panic: boom [recovered]\n"))
}

// TestWatchFatal_UnrecoveredGoroutinePanicLeavesReport runs a child that
// watches for fatal crashes and then panics in a goroutine nothing
// recovers, and checks the next collection turns what the runtime wrote
// into a report.
func TestWatchFatal_UnrecoveredGoroutinePanicLeavesReport(t *testing.T) {
	if os.Getenv("CRASH_FATAL_CHILD") == "1" {
		WatchFatal()
		done := make(chan struct{})
		go func() {
			defer close(done)
			panic("goroutine nobody recovers")
		}()
		<-done
		return
	}
	dir := setupTestDir(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWatchFatal_UnrecoveredGoroutinePanicLeavesReport$")
	cmd.Env = append(os.Environ(), "CRASH_FATAL_CHILD=1", "HARNESS_CRASH_DIR="+dir)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "the child dies of its panic:\n%s", out)

	Collect()

	reports, err := List()
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, "goroutine nobody recovers", reports[0].Panic)
	data, err := os.ReadFile(reports[0].Path)
	require.NoError(t, err)
	require.Contains(t, string(data), "TestWatchFatal_UnrecoveredGoroutinePanicLeavesReport")
}
