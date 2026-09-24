package crash

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A panic nobody recovers - in a goroutine started without Go, or a
// fatal runtime error such as concurrent map writes - kills the process
// before any deferred capture runs, and the trace the runtime prints goes
// to a stderr that the terminal redraws over or nobody was watching. The
// runtime can be told to write that trace to a file as well
// (debug.SetCrashOutput); WatchFatal points it at a pending file in the
// report directory, one per process, which a clean exit removes while
// still empty. A process that died leaves its trace there, and the next
// Harness to start - or `harness crashes` - turns it into an ordinary
// report.

const (
	pendingPrefix = "pending-"
	pendingSuffix = ".crash"
)

// pendingSettle is how long a non-empty pending file of a process that
// may still be alive is left alone: the runtime is writing the trace
// while the process dies, and a file caught mid-write would become half
// a report.
const pendingSettle = 5 * time.Second

// WatchFatal routes the runtime's fatal crash output for this process to
// a pending file in the report directory, after collecting the pending
// files earlier processes left. It returns the function that ends the
// watch on a clean exit, removing the pending file while it is empty;
// the function is safe to call more than once. Everything here is best
// effort: when the file cannot be set up, fatal crashes are only printed,
// as before.
func WatchFatal() (stop func()) {
	Collect()
	noop := func() {}

	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return noop
	}
	path := filepath.Join(dir, fmt.Sprintf("%s%d%s", pendingPrefix, os.Getpid(), pendingSuffix))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return noop
	}
	// The runtime keeps a duplicate of the descriptor, so this one can go
	// right away.
	err = debug.SetCrashOutput(f, debug.CrashOptions{})
	_ = f.Close()
	if err != nil {
		_ = os.Remove(path)
		return noop
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
			if info, err := os.Stat(path); err == nil && info.Size() == 0 {
				_ = os.Remove(path)
			}
		})
	}
}

// Collect turns the pending files of processes that crashed into
// reports, and removes the empty ones processes that are gone left
// behind (a process killed outright never runs its clean exit).
func Collect() {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		pid, ok := pendingPID(name)
		if !ok || pid == os.Getpid() || !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(Dir(), name)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Size() == 0 {
			if !processAlive(pid) {
				_ = os.Remove(path)
			}
			continue
		}
		if time.Since(info.ModTime()) < pendingSettle && processAlive(pid) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if write(info.ModTime(), "fatal", fatalSummary(string(data)), string(data)) == "" {
			// Kept for the next attempt rather than lost.
			continue
		}
		if err := os.Remove(path); err != nil {
			slog.Warn("Failed to remove collected crash output", "path", path, "error", err)
		}
	}
}

// pendingPID parses the process id out of a pending file's name.
func pendingPID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, pendingPrefix)
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, pendingSuffix)
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(rest)
	return pid, err == nil && pid > 0
}

// fatalSummary is the one-line account of a fatal crash trace: the value
// of the panic ("panic: boom" gives "boom", "[recovered]" notes and all)
// or the runtime's fatal error as it printed it.
func fatalSummary(trace string) string {
	for line := range strings.SplitSeq(trace, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if value, ok := strings.CutPrefix(line, "panic: "); ok {
			return value
		}
		return line
	}
	return "(empty crash output)"
}
