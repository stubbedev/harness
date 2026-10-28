package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/term"
)

const (
	// maxSessionJobs caps how many background jobs one session may have
	// running at once. Past it a start fails with a message the model can
	// read: an unbounded fleet would outlive the session that asked for
	// it and spend the machine's process budget on results nobody can
	// collect.
	maxSessionJobs = 8

	// jobOutputTailBytes caps the in-memory tail of a job's output. The
	// full log stays on disk; the tail is what a later wait hands back,
	// so it is bounded by what is worth putting in a context, not by
	// what the command printed.
	jobOutputTailBytes = 64 << 10

	// jobLogPerm is the mode of a job's log file. It holds whatever the
	// command printed, which can be a secret, so only the owner reads it.
	jobLogPerm = 0o600

	// jobLogDirPerm is the mode of the directory those logs live in.
	jobLogDirPerm = 0o755

	// jobWaitDelay bounds how long waiting for a job's output can hold
	// after its process died: a descendant that inherited the output pipe
	// must not keep a finished job looking like a running one.
	jobWaitDelay = 5 * time.Second
)

// JobState is where a background job's life stands. A job is cancellable
// while running and collectable once it is not.
type JobState string

const (
	// JobRunning is a job whose command is still executing.
	JobRunning JobState = "running"
	// JobExited is a job whose command ended with status 0.
	JobExited JobState = "exited"
	// JobFailed is a job whose command ended with a non-zero status or
	// without a status at all.
	JobFailed JobState = "failed"
	// JobCancelled is a job stopped by an explicit cancel, its session's
	// cancel or the coordinator's shutdown.
	JobCancelled JobState = "cancelled"
)

// JobInfo is a point-in-time snapshot of a background job: enough to
// report it, wait on it and collect its result without holding the record
// open. Elapsed is derived from the timestamps taken under the record's
// lock, so it is consistent with them whatever the caller's clock.
type JobInfo struct {
	ID        string
	State     JobState
	Command   string
	ExitCode  *int
	Output    string
	LogPath   string
	StartedAt time.Time
	Finished  time.Time
	Elapsed   time.Duration
	Collected bool
}

// backgroundJob is one detached command a session started: its record
// outlives the call that started it (and the turn), so a later wait can
// collect the result exactly once, and its log path names the file that
// keeps everything the command printed.
type backgroundJob struct {
	id            string
	parentSession string
	command       string
	logPath       string
	cancel        context.CancelFunc

	mu        sync.Mutex
	state     JobState
	exitCode  *int
	started   time.Time
	finished  time.Time
	collected bool
	// out is the job's log sinks, attached once it is ready to run; a
	// record can be looked up before that, and reports no output yet.
	out *jobLog
}

// attachOutput hands the job the log its command writes to.
func (j *backgroundJob) attachOutput(out *jobLog) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.out = out
}

// info returns the job's current state.
func (j *backgroundJob) info() JobInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	info := JobInfo{
		ID:        j.id,
		State:     j.state,
		Command:   j.command,
		ExitCode:  j.exitCode,
		LogPath:   j.logPath,
		StartedAt: j.started,
		Finished:  j.finished,
		Elapsed:   j.elapsedLocked(),
		Collected: j.collected,
	}
	if j.out != nil {
		info.Output = j.out.output()
	}
	return info
}

// elapsedLocked is how long the job ran: the recorded span once finished,
// the time since the start while it is still running. Callers hold mu.
func (j *backgroundJob) elapsedLocked() time.Duration {
	switch {
	case j.started.IsZero():
		return 0
	case j.finished.IsZero():
		return time.Since(j.started)
	default:
		return j.finished.Sub(j.started)
	}
}

// finish records the job's terminal state exactly once, so a duplicate
// finish cannot overwrite a collected result.
func (j *backgroundJob) finish(ctx context.Context, err error, at time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != JobRunning {
		return
	}
	j.finished = at
	// The job's context is cancelled only by an explicit cancel, its
	// session's cancel or a shutdown, so a cancelled job ended because
	// of one; any other ending is the command's own.
	if ctx.Err() != nil {
		j.state = JobCancelled
		return
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code := exitErr.ExitCode()
		j.exitCode = &code
		if code == 0 {
			j.state = JobExited
			return
		}
	}
	if err == nil {
		code := 0
		j.exitCode = &code
		j.state = JobExited
		return
	}
	j.state = JobFailed
}

// cancelRun stops the job's process tree if it is still running, and
// reports whether it was: a finished job has nothing left to cancel.
func (j *backgroundJob) cancelRun() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != JobRunning {
		return false
	}
	j.cancel()
	return true
}

// collect marks a finished job's result as taken, so exactly one wait
// hands it back; a running job has no result to collect yet.
func (j *backgroundJob) collect() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state == JobRunning || j.collected {
		return false
	}
	j.collected = true
	return true
}

// isCollected reports whether a job's result has already been handed out.
func (j *backgroundJob) isCollected() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.collected
}

// jobTail is a fixed-size ring over the last bytes written to it: append
// stays O(1) in the bytes written rather than in the output size, and the
// buffer never grows past its cap.
type jobTail struct {
	buf []byte
	// next is the index the next byte goes to, and once wrapped the
	// index of the oldest byte kept.
	next    int
	wrapped bool
}

func (t *jobTail) append(p []byte) {
	if len(p) == 0 {
		return
	}
	if t.buf == nil {
		t.buf = make([]byte, jobOutputTailBytes)
	}
	if len(p) >= len(t.buf) {
		// One write larger than the whole tail: only its end survives.
		copy(t.buf, p[len(p)-len(t.buf):])
		t.next = 0
		t.wrapped = true
		return
	}
	n := copy(t.buf[t.next:], p)
	if n < len(p) {
		copy(t.buf, p[n:])
	}
	// A write that reaches the end of the buffer fills the ring, even
	// when it lands exactly on the boundary and the next byte goes to
	// index 0 without any of it being copied to the front.
	if n < len(p) || t.next+len(p) >= len(t.buf) {
		t.wrapped = true
	}
	t.next = (t.next + len(p)) % len(t.buf)
}

// String returns the tail's contents in the order they were written.
func (t *jobTail) String() string {
	if !t.wrapped {
		return string(t.buf[:t.next])
	}
	out := make([]byte, 0, len(t.buf))
	out = append(out, t.buf[t.next:]...)
	out = append(out, t.buf[:t.next]...)
	return string(out)
}

// jobLog is a job's combined stdout and stderr: every byte to the on-disk
// log, the last jobOutputTailBytes of them into the in-memory tail.
type jobLog struct {
	mu   sync.Mutex
	file *os.File
	tail jobTail
}

func (l *jobLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Losing a copy of the output is not the command failing: the job's
	// result is what it printed and how it exited, and a full disk must
	// not turn into a failed job.
	if l.file != nil {
		_, _ = l.file.Write(p)
	}
	l.tail.append(p)
	return len(p), nil
}

// output returns the in-memory tail of the job's output.
func (l *jobLog) output() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tail.String()
}

func (l *jobLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// jobRegistry owns every session's background shell jobs, keyed by the
// session that started them: a job id is unique and monotonic within its
// own session, and no session can see another's jobs.
//
// Wakeups use the close-and-replace channels of signalMap (the same
// pattern as liveInbox): a waiter fetches the session's channel before
// reading job state and selects on it after, so a finish landing between
// the read and the wait still wakes it. The channel is broadcast once per
// finish, which is exactly what a wait needs to re-check every job's
// state.
type jobRegistry struct {
	mu      sync.Mutex
	seq     map[string]int
	jobs    map[string]map[string]*backgroundJob
	running map[string]int
	signals signalMap
	// now is the clock job timestamps are taken from; tests replace it to
	// assert elapsed times without sleeping on the wall clock.
	now func() time.Time
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{
		seq:     make(map[string]int),
		jobs:    make(map[string]map[string]*backgroundJob),
		running: make(map[string]int),
		now:     time.Now,
	}
}

// start launches command as a detached background job of parentSession,
// writing its log under logDir. The call's own context is deliberately
// not involved: the job outlives the call, and only an explicit cancel,
// the session's cancel or a shutdown stops it.
func (r *jobRegistry) start(parentSession, command, workingDir, logDir string) (*backgroundJob, error) {
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("a background job needs a command to run")
	}
	shellPath, ok := term.Shell()
	if !ok {
		return nil, errors.New("no shell could be identified to run a background job")
	}

	ctx, cancel := context.WithCancel(context.Background())
	job, err := r.reserve(parentSession, command, logDir, cancel)
	if err != nil {
		cancel()
		return nil, err
	}

	out, err := createJobLog(logDir, job.logPath)
	if err != nil {
		cancel()
		r.drop(parentSession, job.id)
		return nil, err
	}
	job.attachOutput(out)

	cmd := exec.CommandContext(ctx, shellPath, shellArgs(shellPath, command)...)
	cmd.Dir = workingDir
	cmd.Stdout = out
	cmd.Stderr = out

	stop, err := startJobProcess(ctx, cmd)
	if err != nil {
		cancel()
		out.close()
		r.drop(parentSession, job.id)
		return nil, fmt.Errorf("could not start background job: %w", err)
	}
	go r.run(ctx, job, cmd, out, stop)
	return job, nil
}

// reserve mints the session's next job id and adds its record, refusing
// past the per-session cap. The slot is counted here, under the lock, so
// concurrent starts cannot both take the last one.
func (r *jobRegistry) reserve(parentSession, command, logDir string, cancel context.CancelFunc) (*backgroundJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[parentSession] >= maxSessionJobs {
		return nil, fmt.Errorf(
			"this session already has %d background jobs running; wait for one to finish before starting another",
			maxSessionJobs,
		)
	}
	r.seq[parentSession]++
	id := fmt.Sprintf("job-%d", r.seq[parentSession])
	job := &backgroundJob{
		id:            id,
		parentSession: parentSession,
		command:       command,
		logPath:       filepath.Join(logDir, parentSession+"-"+id+".log"),
		cancel:        cancel,
		state:         JobRunning,
		started:       r.now(),
	}
	r.running[parentSession]++
	if r.jobs[parentSession] == nil {
		r.jobs[parentSession] = make(map[string]*backgroundJob)
	}
	r.jobs[parentSession][job.id] = job
	return job, nil
}

// drop forgets a job whose process never started: it never became a
// result, so nothing would collect it.
func (r *jobRegistry) drop(parentSession, jobID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs[parentSession], jobID)
	r.releaseLocked(parentSession)
}

// run waits for a job's command and records how it ended, then wakes
// everyone waiting on the session. It is the only place a job reaches a
// terminal state, so a finish broadcasts exactly once.
func (r *jobRegistry) run(ctx context.Context, job *backgroundJob, cmd *exec.Cmd, out *jobLog, stop func()) {
	err := cmd.Wait()
	stop()
	job.finish(ctx, err, r.now())
	out.close()
	r.mu.Lock()
	r.releaseLocked(job.parentSession)
	r.mu.Unlock()
	r.signals.Broadcast(job.parentSession)
}

// releaseLocked frees a running slot. Callers hold mu.
func (r *jobRegistry) releaseLocked(parentSession string) {
	if n := r.running[parentSession]; n > 1 {
		r.running[parentSession] = n - 1
	} else {
		delete(r.running, parentSession)
	}
}

// cancel stops one running job and leaves its record in place, so its
// cancelled state can still be collected.
func (r *jobRegistry) cancel(parentSession, jobID string) error {
	job := r.lookup(parentSession, jobID)
	if job == nil {
		return fmt.Errorf("unknown background job %q", jobID)
	}
	if !job.cancelRun() {
		return fmt.Errorf("background job %s already finished", jobID)
	}
	return nil
}

// cancelSession stops every running job of a session and forgets its
// records: the session is over, so nothing would wait on or collect them.
// The running count goes with the records; a finishing job's release is
// a no-op after that.
func (r *jobRegistry) cancelSession(parentSession string) {
	r.mu.Lock()
	jobs := r.jobs[parentSession]
	delete(r.jobs, parentSession)
	delete(r.seq, parentSession)
	delete(r.running, parentSession)
	r.mu.Unlock()
	for _, job := range jobs {
		job.cancelRun()
	}
}

// cancelAll stops every job of every session: the coordinator is going
// away and no job may outlive it.
func (r *jobRegistry) cancelAll() {
	r.mu.Lock()
	sessions := make([]string, 0, len(r.jobs))
	for session := range r.jobs {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	for _, session := range sessions {
		r.cancelSession(session)
	}
}

// lookup returns a session's job record, or nil when that session never
// started the id: job ids are per session, so another session's job is
// indistinguishable from one that does not exist.
func (r *jobRegistry) lookup(parentSession, jobID string) *backgroundJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jobs[parentSession][jobID]
}

// collect takes a finished job's result, handing it out at most once.
func (r *jobRegistry) collect(parentSession, jobID string) bool {
	job := r.lookup(parentSession, jobID)
	return job != nil && job.collect()
}

// signalChan returns the session's current wakeup channel. Fetch it
// before reading job state and select on it after.
func (r *jobRegistry) signalChan(parentSession string) chan struct{} {
	return r.signals.Chan(parentSession)
}

// StartBackgroundJob implements tools.BackgroundJobStarter: it starts
// command as a job of sessionID's session and returns the id and log path
// the tool call reports back. ctx is deliberately unused: the call's own
// cancellation must not kill a job that is meant to outlive it.
func (c *coordinator) StartBackgroundJob(_ context.Context, sessionID, command, workingDir string) (tools.BackgroundJob, error) {
	job, err := c.jobs.start(sessionID, command, workingDir, c.jobLogDir())
	if err != nil {
		return tools.BackgroundJob{}, err
	}
	info := job.info()
	return tools.BackgroundJob{ID: info.ID, LogPath: info.LogPath}, nil
}

// jobLogDir is where background job logs live: a jobs directory in the
// workspace's data directory, beside its session state and logs.
func (c *coordinator) jobLogDir() string {
	return filepath.Join(c.cfg.Config().Options.DataDirectory, "jobs")
}

// createJobLog creates a job's log file, and with it the directory the
// logs live in. The file is created at start rather than on the first
// write so the path a response names always exists.
func createJobLog(logDir, path string) (*jobLog, error) {
	if err := os.MkdirAll(logDir, jobLogDirPerm); err != nil {
		return nil, fmt.Errorf("could not create the background job log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, jobLogPerm)
	if err != nil {
		return nil, fmt.Errorf("could not create the background job log: %w", err)
	}
	return &jobLog{file: file}, nil
}

// shellArgs is the command line that runs command as one line in the
// shell at path: each dialect spells "run this string" differently.
func shellArgs(shellPath, command string) []string {
	switch term.KindOf(shellPath) {
	case term.KindPowerShell:
		return []string{"-NoProfile", "-Command", command}
	case term.KindCmd:
		return []string{"/C", command}
	default:
		return []string{"-c", command}
	}
}
