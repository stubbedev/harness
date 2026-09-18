package extensions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
)

const (
	// DefaultJobTimeout bounds a background job when its registration
	// names no timeout. Jobs exist for work too slow to hold a tool call
	// open, so the bound is minutes rather than the seconds a call gets.
	DefaultJobTimeout = 10 * time.Minute
	// MaxJobTimeout caps an explicit timeout, so a typo cannot leave a
	// goroutine and a VM alive for the life of the process.
	MaxJobTimeout = time.Hour
	// defaultMaxConcurrentJobs bounds jobs running at once across every
	// extension. Starts beyond it wait for a slot.
	defaultMaxConcurrentJobs = 8
	// maxJobRecords bounds the queue. When it is full the oldest
	// finished record is dropped; a running job is never dropped.
	maxJobRecords = 100
)

// JobState is where a background job is in its life.
type JobState string

const (
	JobRunning  JobState = "running"
	JobDone     JobState = "done"
	JobFailed   JobState = "failed"
	JobCanceled JobState = "canceled"
)

// Job is one background run of a registered job handler. Records outlive
// the run so a result can be collected later — that queue is the point
// of jobs.
type Job struct {
	ID        string    `json:"id"`
	Extension string    `json:"extension"`
	Name      string    `json:"name"`
	State     JobState  `json:"state"`
	Result    string    `json:"result,omitempty"`
	Err       string    `json:"error,omitempty"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished,omitzero"`
	// Collected records whether the result has been handed to the model
	// (or to Lua) already, so a drain returns each result once.
	Collected bool `json:"collected"`

	cancel context.CancelFunc
	done   chan struct{}
}

// Age is how long the job has been running, or how long it ran.
func (j Job) Age() time.Duration {
	if j.Finished.IsZero() {
		return time.Since(j.Started)
	}
	return j.Finished.Sub(j.Started)
}

// jobSpec is a job handler an extension registered while loading.
type jobSpec struct {
	name        string
	description string
	timeout     time.Duration
	fn          *lua.LFunction
}

// jobRunner owns the background jobs of every extension in a host: the
// records, the queue of finished ones, and the slots they run in.
type jobRunner struct {
	host *Host

	mu    sync.Mutex
	jobs  map[string]*Job
	order []string

	slots chan struct{}
}

func newJobRunner(host *Host, maxConcurrent int) *jobRunner {
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentJobs
	}
	return &jobRunner{
		host:  host,
		jobs:  make(map[string]*Job),
		slots: make(chan struct{}, maxConcurrent),
	}
}

// start launches a job handler in a VM of its own and returns its ID
// immediately.
//
// The handler runs in a fresh instance of the same extension rather than
// in the VM that asked for it: that VM is mid-call (it is the caller),
// and a second entry into it would deadlock. The fresh instance re-runs
// init.lua, so an init.lua should register and nothing more.
//
// The context is deliberately detached from the calling tool call. A job
// outliving the call that started it is the whole point; it ends on its
// own timeout, on cancel, or when the host shuts down.
func (r *jobRunner) start(ext *Extension, name string, args any, timeout time.Duration) *Job {
	id := jobID()
	ctx, cancel := context.WithTimeout(r.host.jobContext(), timeout)

	job := &Job{
		ID:        id,
		Extension: ext.Name,
		Name:      name,
		State:     JobRunning,
		Started:   time.Now(),
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	r.record(job)

	go func() {
		defer cancel()
		defer close(job.done)

		select {
		case r.slots <- struct{}{}:
			defer func() { <-r.slots }()
		case <-ctx.Done():
			r.finish(job, "", ctx.Err())
			return
		}

		result, err := r.run(ctx, ext, name, args)
		r.finish(job, result, err)
	}()

	return job
}

// run loads a private VM for the extension and calls the named handler
// in it.
func (r *jobRunner) run(ctx context.Context, ext *Extension, name string, args any) (string, error) {
	worker, err := spawnLoadedInstance(ctx, r.host, ext)
	if err != nil {
		return "", err
	}
	defer worker.close()

	spec, ok := worker.jobSpecs[name]
	if !ok {
		return "", fmt.Errorf("extension %q registers no job %q", ext.Name, name)
	}

	value, err := worker.callWithin(ctx, MaxJobTimeout, spec.fn, toLua(worker.L, args))
	if err != nil {
		return "", err
	}
	return jobResultString(value), nil
}

// jobResultString renders a handler's return value for the queue. A
// table is stored as JSON so the model reads it as data.
func jobResultString(value lua.LValue) string {
	switch typed := value.(type) {
	case *lua.LNilType:
		return ""
	case *lua.LTable:
		data, err := marshalLuaValue(typed)
		if err != nil {
			return typed.String()
		}
		return string(data)
	default:
		return value.String()
	}
}

// record files a new job, trimming the oldest finished one when the
// queue is full. A running job is never dropped, so a full queue of
// running work simply grows until something finishes.
func (r *jobRunner) record(job *Job) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.jobs[job.ID] = job
	r.order = append(r.order, job.ID)

	for len(r.order) > maxJobRecords {
		index := slices.IndexFunc(r.order, func(id string) bool {
			existing, ok := r.jobs[id]
			return ok && existing.State != JobRunning
		})
		if index < 0 {
			return
		}
		delete(r.jobs, r.order[index])
		r.order = slices.Delete(r.order, index, index+1)
	}
}

// finish files the outcome of a run.
func (r *jobRunner) finish(job *Job, result string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	job.Finished = time.Now()
	job.Result = result
	switch {
	case err == nil:
		job.State = JobDone
	case canceled(err):
		job.State = JobCanceled
		job.Err = err.Error()
	default:
		job.State = JobFailed
		job.Err = err.Error()
	}
	if err != nil {
		slog.Debug(
			"Extension job finished with an error",
			"extension", job.Extension,
			"job", job.Name,
			"id", job.ID,
			"error", err,
		)
	}
}

// get returns a snapshot of one job.
func (r *jobRunner) get(id string) (Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *job, true
}

// list returns a snapshot of every job, oldest first.
func (r *jobRunner) list() []Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Job, 0, len(r.order))
	for _, id := range r.order {
		if job, ok := r.jobs[id]; ok {
			out = append(out, *job)
		}
	}
	return out
}

// drain returns every finished job whose result has not been collected
// yet, marking them collected. This is the queue: slow work finishes on
// its own schedule and the results wait here until something asks.
func (r *jobRunner) drain() []Job {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []Job
	for _, id := range r.order {
		job, ok := r.jobs[id]
		if !ok || job.State == JobRunning || job.Collected {
			continue
		}
		job.Collected = true
		out = append(out, *job)
	}
	return out
}

// collect marks one job's result collected and returns it.
func (r *jobRunner) collect(id string) (Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return Job{}, false
	}
	if job.State != JobRunning {
		job.Collected = true
	}
	return *job, true
}

// wait blocks until the job finishes or the context ends, then returns
// its final state.
func (r *jobRunner) wait(ctx context.Context, id string) (Job, bool) {
	r.mu.Lock()
	job, ok := r.jobs[id]
	r.mu.Unlock()
	if !ok {
		return Job{}, false
	}
	select {
	case <-job.done:
	case <-ctx.Done():
	}
	return r.collect(id)
}

// cancel stops a running job.
func (r *jobRunner) cancel(id string) bool {
	r.mu.Lock()
	job, ok := r.jobs[id]
	running := ok && job.State == JobRunning
	r.mu.Unlock()
	if !running {
		return false
	}
	job.cancel()
	return true
}

// cancelAll stops every running job, for host shutdown.
func (r *jobRunner) cancelAll() {
	r.mu.Lock()
	var running []*Job
	for _, job := range r.jobs {
		if job.State == JobRunning {
			running = append(running, job)
		}
	}
	r.mu.Unlock()

	for _, job := range running {
		job.cancel()
	}
}

// jobID returns a short, unique-enough identifier for a job.
func jobID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("job_%d", time.Now().UnixNano())
	}
	return "job_" + hex.EncodeToString(buf[:])
}

// canceled reports whether an error is a cancellation rather than a
// failure of the work itself. A cancellation raised inside the VM
// arrives as a Lua error carrying the context's message, so the string
// is checked alongside the sentinel.
func canceled(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, context.Canceled.Error()) ||
		strings.Contains(message, context.DeadlineExceeded.Error())
}
