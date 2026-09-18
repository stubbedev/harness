package shell

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// Stream hardening bounds: silence is not proof of life. A run that
// stops producing output for streamIdleTimeout is killed as wedged
// rather than held open forever, and no run outlives streamMaxRuntime.
// Both are vars so tests can shorten them.
var (
	streamIdleTimeout = 2 * time.Minute
	streamMaxRuntime  = 10 * time.Minute
)

// streamChunkInterval is the fastest onProgress fires: chunks arriving
// faster are batched into the next push, so a flooding command (`yes`,
// dd from /dev/zero) cannot flood the consumer in turn. Batched bytes
// are not lost to the consumer of the final result - Output carries
// everything captured.
const streamChunkInterval = 50 * time.Millisecond

// maxCaptureBytes caps the memory one captured run can hold. Past it
// the head is dropped, keeping the tail - where a diagnosis usually
// lives - and a marker says how much went.
const maxCaptureBytes = 8 << 20

// progressWriter collects a run's stdout and stderr, tracking the last
// write as the watchdog's notion of life and bounding its own memory.
// It is safe for concurrent use (stdout and stderr write
// simultaneously).
type progressWriter struct {
	mu         sync.Mutex
	buf        []byte
	dropped    int64
	pending    []byte
	onProgress func(string)
	lastWrite  time.Time
	lastPush   time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	// Trim with hysteresis so the cap is not re-hit on every write:
	// overshoot by an eighth before cutting back to the cap.
	if excess := len(w.buf) - (maxCaptureBytes + maxCaptureBytes/8); excess > 0 {
		w.buf = w.buf[excess:]
		w.dropped += int64(excess)
	}
	w.lastWrite = time.Now()
	w.pending = append(w.pending, p...)
	if w.onProgress != nil && (w.lastPush.IsZero() || w.lastWrite.Sub(w.lastPush) >= streamChunkInterval) {
		w.flushLocked()
	}
	return len(p), nil
}

// flushLocked pushes the batched chunks; the caller holds the lock.
func (w *progressWriter) flushLocked() {
	if w.onProgress == nil || len(w.pending) == 0 {
		return
	}
	w.onProgress(string(w.pending))
	w.pending = nil
	w.lastPush = w.lastWrite
}

// flush pushes whatever is still batched (the quiet tail after a flood).
func (w *progressWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
}

// lastActivity reports when output last arrived.
func (w *progressWriter) lastActivity() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastWrite
}

// output returns the captured output, capped to the tail with a marker
// when the head had to go.
func (w *progressWriter) output() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dropped > 0 {
		return fmt.Sprintf("… %d earlier bytes dropped …\n%s", w.dropped, w.buf)
	}
	return string(w.buf)
}

// watchdogReason records which bound ended the run, once.
type watchdogReason struct {
	mu     sync.Mutex
	reason string
}

func (w *watchdogReason) set(reason string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reason == "" {
		w.reason = reason
	}
}

func (w *watchdogReason) get() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reason
}

// RunAndCaptureStream executes a shell command and streams output chunks
// to onProgress as they arrive. Returns the complete output and exit
// code. The run is bounded: silent past streamIdleTimeout or older than
// streamMaxRuntime it is killed and the output says so, capture memory
// is capped, and onProgress is coalesced - so no command can wedge the
// caller or flood it, whatever it does to its own output streams.
func RunAndCaptureStream(ctx context.Context, opts RunOptions, onProgress func(string)) (CaptureResult, error) {
	if opts.Env == nil {
		opts.Env = os.Environ()
	}
	opts.Env = append(opts.Env, ptyColorEnvVars...)

	buf := &progressWriter{onProgress: onProgress, lastWrite: time.Now()}
	opts.Stdout = buf
	opts.Stderr = buf

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wd := &watchdogReason{}
	started := time.Now()
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-tick.C:
				if idle := time.Since(buf.lastActivity()); idle >= streamIdleTimeout {
					wd.set(fmt.Sprintf("no output for %s", idle.Round(time.Second)))
					cancel()
					return
				}
				if time.Since(started) >= streamMaxRuntime {
					wd.set(fmt.Sprintf("still running after %s", streamMaxRuntime))
					cancel()
					return
				}
			}
		}
	}()

	runErr := Run(runCtx, opts)
	buf.flush()

	exitCode := 0
	if runErr != nil {
		exitCode = ExitCode(runErr)
	}
	output := buf.output()
	if reason := wd.get(); reason != "" && ctx.Err() == nil {
		output += fmt.Sprintf("\n[command killed by harness: %s]\n", reason)
	}

	return CaptureResult{
		Output:   output,
		ExitCode: exitCode,
	}, nil
}
