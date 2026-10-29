package agent

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// requireJobShell skips tests that run commands through a background job:
// they are written for a POSIX shell, which is what term.Shell identifies
// on the platforms the suite drives commands on.
func requireJobShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("background job tests drive a POSIX shell")
	}
}

func newTestJobRegistry(t *testing.T) (*jobRegistry, string) {
	t.Helper()
	return newJobRegistry(), t.TempDir()
}

// runningCount is a session's live job count, read under the registry's
// lock the way the registry reads it.
func (r *jobRegistry) runningCount(session string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[session]
}

// waitFinished blocks until a job reaches a terminal state and returns
// its snapshot: every test that runs a command is bounded by this, so a
// job that never finishes fails the test instead of hanging it.
func waitFinished(t *testing.T, job *backgroundJob) JobInfo {
	t.Helper()
	var info JobInfo
	require.Eventually(t, func() bool {
		info = job.info()
		return info.State != JobRunning
	}, 30*time.Second, 10*time.Millisecond)
	return info
}

// A job that exits 0 is observable as finished, with everything it
// printed - stdout and stderr alike - its exit code and its log.
func TestJobExitsWithCombinedOutput(t *testing.T) {
	t.Parallel()
	requireJobShell(t)

	reg, logDir := newTestJobRegistry(t)
	job, err := reg.start("s", "echo to-stdout; echo to-stderr >&2", t.TempDir(), logDir)
	require.NoError(t, err)

	info := waitFinished(t, job)
	require.Equal(t, "job-1", info.ID)
	require.Equal(t, JobExited, info.State)
	require.NotNil(t, info.ExitCode)
	require.Equal(t, 0, *info.ExitCode)
	require.Contains(t, info.Output, "to-stdout")
	require.Contains(t, info.Output, "to-stderr")
	require.NotZero(t, info.Finished)
	require.GreaterOrEqual(t, info.Elapsed, time.Duration(0))
	require.NotZero(t, info.Elapsed, "a finished job records how long it ran")

	logged, err := os.ReadFile(info.LogPath)
	require.NoError(t, err)
	require.Equal(t, info.Output, string(logged))
	info2, err := os.Stat(info.LogPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(jobLogPerm), info2.Mode().Perm())

	require.True(t, reg.collect("s", info.ID))
	require.True(t, job.isCollected())
	require.False(t, reg.collect("s", info.ID), "a finished job's result is handed out once")
}

// A failing command is a finished job too: it keeps the non-zero status
// and what it printed before it died.
func TestJobFailureKeepsExitCodeAndOutput(t *testing.T) {
	t.Parallel()
	requireJobShell(t)

	reg, logDir := newTestJobRegistry(t)
	job, err := reg.start("s", "echo about-to-fail >&2; exit 3", t.TempDir(), logDir)
	require.NoError(t, err)

	info := waitFinished(t, job)
	require.Equal(t, JobFailed, info.State)
	require.NotNil(t, info.ExitCode)
	require.Equal(t, 3, *info.ExitCode)
	require.Contains(t, info.Output, "about-to-fail")
}

// One session cannot see another's jobs: ids are minted per session, so
// the same id names a different job in each, and an id no job of the
// session holds is unknown.
func TestJobsAreScopedToTheirSession(t *testing.T) {
	t.Parallel()
	requireJobShell(t)

	reg, logDir := newTestJobRegistry(t)
	workDir := t.TempDir()
	first, err := reg.start("a", "echo from-a", workDir, logDir)
	require.NoError(t, err)
	second, err := reg.start("b", "echo from-b", workDir, logDir)
	require.NoError(t, err)

	require.Equal(t, "job-1", first.info().ID)
	require.Equal(t, "job-1", second.info().ID)
	require.NotSame(t, first, second)

	require.Same(t, first, reg.lookup("a", "job-1"))
	require.Same(t, second, reg.lookup("b", "job-1"))

	require.Nil(t, reg.lookup("a", "job-2"), "a session sees only the ids it minted")
	require.Nil(t, reg.lookup("nobody", "job-1"))

	waitFinished(t, first)
	waitFinished(t, second)
	require.False(t, reg.collect("a", "job-2"), "another session's id is not this session's result")
}

// The per-session cap is a real bound: the ninth running job is refused
// with a message the model can read, and finishing one frees a slot.
func TestJobCapRefusesTheNinth(t *testing.T) {
	t.Parallel()
	requireJobShell(t)

	reg, logDir := newTestJobRegistry(t)
	workDir := t.TempDir()
	jobs := make([]*backgroundJob, 0, maxSessionJobs)
	for range maxSessionJobs {
		job, err := reg.start("s", "sleep 60", workDir, logDir)
		require.NoError(t, err)
		jobs = append(jobs, job)
	}

	_, err := reg.start("s", "sleep 60", workDir, logDir)
	require.Error(t, err)
	require.Contains(t, err.Error(), strconv.Itoa(maxSessionJobs))
	require.Contains(t, err.Error(), "wait for one to finish")

	// The cap is per session: another session still has room.
	other, err := reg.start("other", "sleep 60", workDir, logDir)
	require.NoError(t, err)

	// Cancelling records is not enough to free a slot: only a finished
	// job does. Cancel through the registry and wait for the slots back.
	for _, job := range jobs {
		require.NoError(t, reg.cancel("s", job.info().ID))
	}
	for _, job := range jobs {
		require.Equal(t, JobCancelled, waitFinished(t, job).State)
	}
	_, err = reg.start("s", "echo again", workDir, logDir)
	require.NoError(t, err)

	reg.cancelSession("s")
	reg.cancelSession("other")
	require.Equal(t, JobCancelled, waitFinished(t, other).State)
}

// The in-memory tail is bounded; the log on disk keeps everything.
func TestJobOutputTailIsTruncatedButLogIsNot(t *testing.T) {
	t.Parallel()
	requireJobShell(t)

	reg, logDir := newTestJobRegistry(t)
	job, err := reg.start("s", "head -c 200000 /dev/zero | tr '\\0' x", t.TempDir(), logDir)
	require.NoError(t, err)

	info := waitFinished(t, job)
	require.Len(t, info.Output, jobOutputTailBytes)
	require.Equal(t, strings.Repeat("x", jobOutputTailBytes), info.Output)

	logged, err := os.ReadFile(info.LogPath)
	require.NoError(t, err)
	require.Len(t, logged, 200000, "the log keeps what the tail dropped")
}

// jobTail keeps the last bytes written, in order, whatever the shape of
// the writes; its buffer never grows past the cap.
func TestJobTailKeepsTheLastBytes(t *testing.T) {
	t.Parallel()

	var tail jobTail
	total := jobOutputTailBytes*2 + 123
	written := make([]byte, total)
	for i := range written {
		written[i] = byte('a' + i%26)
	}
	// Varying write sizes, including ones that straddle the ring's wrap,
	// exercise both copy branches.
	for off := 0; off < total; {
		step := min(1+off%1000, total-off)
		tail.append(written[off : off+step])
		off += step
	}
	require.Len(t, tail.String(), jobOutputTailBytes)
	require.Equal(t, string(written[total-jobOutputTailBytes:]), tail.String())
	require.LessOrEqual(t, len(tail.buf), jobOutputTailBytes)

	// A single write larger than the whole tail keeps only its end.
	var big jobTail
	big.append(written)
	require.Equal(t, string(written[total-jobOutputTailBytes:]), big.String())
}

// A job's finish wakes everyone waiting on its session, once, on the
// close-and-replace channel a later wait will select on.
func TestJobFinishWakesTheSessionOnce(t *testing.T) {
	t.Parallel()
	requireJobShell(t)

	reg, logDir := newTestJobRegistry(t)
	workDir := t.TempDir()

	wake := reg.signalChan("s")
	job, err := reg.start("s", "echo done", workDir, logDir)
	require.NoError(t, err)
	select {
	case <-wake:
	case <-time.After(30 * time.Second):
		require.Fail(t, "the finish of a job did not wake the session")
	}
	require.Equal(t, JobExited, waitFinished(t, job).State)

	// The broadcast replaced the channel rather than leaving a closed one
	// behind, so the next finish is a distinct wakeup.
	next := reg.signalChan("s")
	select {
	case <-next:
		require.Fail(t, "the channel fired without a second finish")
	default:
	}

	_, err = reg.start("s", "echo again", workDir, logDir)
	require.NoError(t, err)
	select {
	case <-next:
	case <-time.After(30 * time.Second):
		require.Fail(t, "the second finish did not wake the session")
	}
}

// Cancelling a session cancels the jobs it is still running and forgets
// them: a cancelled turn leaves no job behind to wait on or collect.
func TestJobSessionCancelCancelsRunningJobs(t *testing.T) {
	t.Parallel()
	requireJobShell(t)

	reg, logDir := newTestJobRegistry(t)
	workDir := t.TempDir()
	job, err := reg.start("s", "sleep 60", workDir, logDir)
	require.NoError(t, err)
	done, err := reg.start("s", "echo finished", workDir, logDir)
	require.NoError(t, err)
	waitFinished(t, done)

	wake := reg.signalChan("s")
	reg.cancelSession("s")

	select {
	case <-wake:
	case <-time.After(30 * time.Second):
		require.Fail(t, "cancelling a session did not wake its waiters")
	}
	require.Equal(t, JobCancelled, job.info().State)
	require.Zero(t, reg.lookup("s", job.info().ID), "a finished session's records are gone")
	require.Zero(t, reg.runningCount("s"))
}

// An explicit cancel stops a running job and keeps its record, so the
// cancelled result is still collectable; a finished or unknown job cannot
// be cancelled at all.
func TestJobExplicitCancelKeepsTheRecord(t *testing.T) {
	t.Parallel()
	requireJobShell(t)

	reg, logDir := newTestJobRegistry(t)
	workDir := t.TempDir()
	job, err := reg.start("explicit", "sleep 60", workDir, logDir)
	require.NoError(t, err)

	require.NoError(t, reg.cancel("explicit", job.info().ID))
	info := waitFinished(t, job)
	require.Equal(t, JobCancelled, info.State)
	require.Nil(t, info.ExitCode)
	require.Same(t, job, reg.lookup("explicit", info.ID))
	require.True(t, reg.collect("explicit", info.ID))

	require.Error(t, reg.cancel("explicit", info.ID), "a finished job has nothing left to cancel")
	require.ErrorContains(t, reg.cancel("explicit", "job-99"), "unknown background job")
}

// A job with nothing to run or no shell to run it in is refused before
// anything is started.
func TestJobStartRefusesEmptyCommand(t *testing.T) {
	t.Parallel()

	reg, logDir := newTestJobRegistry(t)
	_, err := reg.start("s", "   ", t.TempDir(), logDir)
	require.ErrorContains(t, err, "needs a command")
	require.Zero(t, reg.runningCount("s"), "a refused start takes no slot")
}

// TestRunningJobsReportsEveryJobOldestFirst pins the strip's feed: a
// session's jobs in mint order, terminal ones included so a viewer can
// reap a row, and without the output a status viewer never reads.
func TestRunningJobsReportsEveryJobOldestFirst(t *testing.T) {
	t.Parallel()
	requireJobShell(t)

	reg, logDir := newTestJobRegistry(t)
	dir := t.TempDir()
	first, err := reg.start("s", "echo one", dir, logDir)
	require.NoError(t, err)
	waitFinished(t, first)
	_, err = reg.start("s", "echo two", dir, logDir)
	require.NoError(t, err)

	c := &coordinator{jobs: reg}
	infos := c.RunningJobs("s")
	require.Len(t, infos, 2)
	require.Equal(t, "job-1", infos[0].ID)
	require.Equal(t, JobExited, infos[0].State)
	require.Empty(t, infos[0].Output, "a feed entry carries no output")
	require.Equal(t, "job-2", infos[1].ID)
	require.Equal(t, JobRunning, infos[1].State)
	require.Equal(t, "echo two", infos[1].Command)

	require.Empty(t, c.RunningJobs("other"), "another session's jobs stay its own")
}
