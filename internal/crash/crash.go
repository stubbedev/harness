// Package crash persists panic reports so that a crash leaves a durable
// artifact even when the process dies or the terminal that briefly showed
// the stack trace closes. Reports go to a single global directory that
// outlives any one workspace and can be listed with `harness crashes`.
//
// Everything here is best effort: capturing a panic must never itself
// panic, and a failing write degrades to the slog record alone.
package crash

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stubbedev/harness/internal/home"
	"github.com/stubbedev/harness/internal/version"
)

// maxReports caps how many reports are kept; older ones are pruned on
// write so an unattended crash loop cannot grow the directory forever.
const maxReports = 50

// reportFilePattern is the layout of a report's file name. The leading
// timestamp makes names sort chronologically, which pruning relies on.
const reportFilePattern = "20060102-150405.000"

// reportTimeFormat is millisecond-precision RFC3339, so two reports from
// the same second still order correctly when listed.
const reportTimeFormat = "2006-01-02T15:04:05.000Z07:00"

var mu sync.Mutex

// written counts the reports this process has written, so a caller can
// tell whether a crash it observed left one behind (see Written).
var written atomic.Uint64

// Dir returns the directory crash reports are written to. The
// HARNESS_CRASH_DIR environment variable overrides the default location
// under the global data root.
func Dir() string {
	if dir := os.Getenv("HARNESS_CRASH_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(home.DataDir(), "crashes")
}

// Capture writes a report describing r and returns its path, or an empty
// string when the report could not be written. Call it from a recovering
// defer so the stack still contains the panicking frames.
func Capture(component string, r any) string {
	return write(component, fmt.Sprint(r), string(debug.Stack()))
}

// CaptureText writes a report for a panic known only by what was
// printed about it - a value and a stack trace that some other
// recovery already formatted - and returns its path, or an empty string
// when it could not be written.
func CaptureText(component, panicValue, stack string) string {
	return write(component, panicValue, stack)
}

// Written reports how many reports this process has written so far.
func Written() uint64 {
	return written.Load()
}

// write persists one report and returns its path, or an empty string
// when it could not be written.
func write(component, panicValue, stack string) string {
	now := time.Now()

	var b strings.Builder
	fmt.Fprintf(&b, "time: %s\n", now.Format(reportTimeFormat))
	fmt.Fprintf(&b, "component: %s\n", component)
	fmt.Fprintf(&b, "version: %s (commit %s, build %s)\n", version.Version, version.Commit, version.BuildID)
	fmt.Fprintf(&b, "go: %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if cwd, err := os.Getwd(); err == nil {
		fmt.Fprintf(&b, "cwd: %s\n", cwd)
	}
	fmt.Fprintf(&b, "goroutines: %d\n", runtime.NumGoroutine())
	fmt.Fprintf(&b, "\npanic: %s\n\n", panicValue)
	fmt.Fprintf(&b, "stack:\n%s\n", stack)

	mu.Lock()
	defer mu.Unlock()

	// The directory is created on first use: a machine that has never
	// crashed has none, and a report that cannot be written is exactly
	// the one nobody sees again.
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		slog.Error("Failed to create crash report directory", "component", component, "error", err)
		return ""
	}
	path := reportPath(now, component)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		slog.Error("Failed to write crash report", "component", component, "error", err)
		return ""
	}
	written.Add(1)
	pruneLocked()
	slog.Error("Captured panic report", "component", component, "panic", panicValue, "path", path)
	return path
}

// reportPath builds the destination for a report, dodging an existing
// file from the same millisecond with a numeric suffix so simultaneous
// captures cannot overwrite each other. It is called with mu held.
func reportPath(now time.Time, component string) string {
	base := now.Format(reportFilePattern)
	safe := sanitize(component)
	for seq := 1; ; seq++ {
		name := fmt.Sprintf("%s-%s.log", base, safe)
		if seq > 1 {
			name = fmt.Sprintf("%s-%d-%s.log", base, seq, safe)
		}
		path := filepath.Join(Dir(), name)
		if _, err := os.Stat(path); err != nil {
			return path
		}
	}
}

// Recover recovers a pending panic, captures a report for it, and then
// runs cleanup regardless of whether the report could be written. Use it
// in a defer where a panic should be contained rather than re-raised.
func Recover(component string, cleanup func()) {
	r := recover()
	if r == nil {
		return
	}
	Capture(component, r)
	if cleanup != nil {
		cleanup()
	}
}

// Go runs fn in a goroutine whose panics are captured as reports
// instead of killing the process. The goroutine still dies after a
// panic; use it for long-lived loops and other routines whose crash
// should be recorded and contained rather than take the whole app down.
// It returns the started goroutine's completion as a channel so callers
// that need to join can.
func Go(component string, fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer Recover(component, nil)
		fn()
	}()
	return done
}

// pruneLocked removes the oldest reports beyond maxReports. It is called
// with mu held.
func pruneLocked() {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".log") {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= maxReports {
		return
	}
	// Names start with a timestamp, so lexical order is chronological.
	names = names[:len(names)-maxReports]
	for _, name := range names {
		if err := os.Remove(filepath.Join(Dir(), name)); err != nil {
			slog.Warn("Failed to prune crash report", "name", name, "error", err)
		}
	}
}

// sanitize keeps a component name usable inside a file name.
func sanitize(component string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', ' ':
			return '-'
		}
		return r
	}, component)
}

// Report describes one persisted crash report.
type Report struct {
	Name      string
	Path      string
	Time      time.Time
	Component string
	Panic     string
}

// List returns the persisted reports, newest first.
func List() ([]Report, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return nil, err
	}
	var reports []Report
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		report := Report{Name: entry.Name(), Path: filepath.Join(Dir(), entry.Name()), Time: info.ModTime()}
		readHeader(&report)
		reports = append(reports, report)
	}
	slices.SortFunc(reports, func(a, b Report) int {
		return b.Time.Compare(a.Time)
	})
	return reports, nil
}

// readHeader fills the Component, Panic, and Time fields from a report's
// leading header lines, preferring the recorded crash time over the
// file's modification time. Reports are small, so reading the whole file
// is fine.
func readHeader(report *Report) {
	data, err := os.ReadFile(report.Path)
	if err != nil {
		return
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if component, ok := strings.CutPrefix(line, "component: "); ok {
			report.Component = component
		}
		if panicValue, ok := strings.CutPrefix(line, "panic: "); ok {
			report.Panic = panicValue
		}
		if stamp, ok := strings.CutPrefix(line, "time: "); ok {
			if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
				report.Time = parsed
			}
		}
	}
}
