package cmd

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/crash"
)

// A panic anywhere in the TUI is recovered by bubbletea, which restores
// the terminal, prints the panic and its stack to stderr and kills the
// program. That print is gone as soon as the terminal redraws, so the
// panic is persisted as a crash report on the way: by the wrapped model
// for Init, Update, View, the input filter and every command they return
// (commands inside a batch or a sequence included), and - for a panic
// none of those saw, in bubbletea's renderer or anywhere else it runs
// code of its own - from the stack bubbletea printed, read off a tee of
// stderr kept while the program runs.

// newPanicCapturingModel wraps the TUI model so a panic in Init, Update,
// View, or a command they return is persisted with its stack trace before
// bubbletea's own recovery swallows it. The panic is re-raised after
// capture so bubbletea's graceful shutdown still runs.
func newPanicCapturingModel(model tea.Model) tea.Model {
	return panicCapturingModel{model}
}

type panicCapturingModel struct {
	tea.Model
}

// captureTUIPanic is deferred around the wrapped model's methods and
// commands. It persists the panic with its stack, then re-raises it.
func captureTUIPanic() {
	if r := recover(); r != nil {
		crash.Capture("tui", r)
		panic(r)
	}
}

// captureTUICmd wraps a command so a panic while it runs in one of
// bubbletea's command goroutines is captured too, and so are the
// commands its result carries: tea.Batch and tea.Sequence return a
// command whose message is the list of commands bubbletea runs next,
// each in a goroutine of its own, where a wrapper around the outer
// command no longer reaches.
func captureTUICmd(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		defer captureTUIPanic()
		msg := cmd()
		wrapCarriedCmds(msg)
		return msg
	}
}

// cmdType is the type of the elements of a message that carries
// commands.
var cmdType = reflect.TypeFor[tea.Cmd]()

// wrapCarriedCmds wraps, in place, each command of a message that is a
// list of commands. tea.BatchMsg is such a list, and so is the message
// of tea.Sequence, whose type bubbletea does not export - hence the
// reflection: both are slices of tea.Cmd, and a slice's elements can be
// replaced whatever its named type.
func wrapCarriedCmds(msg tea.Msg) {
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Type().Elem() != cmdType {
		return
	}
	for i := range v.Len() {
		el := v.Index(i)
		if c, ok := reflect.TypeAssert[tea.Cmd](el); ok && c != nil && el.CanSet() {
			el.Set(reflect.ValueOf(captureTUICmd(c)))
		}
	}
}

// captureTUIFilter wraps the program's input filter, which bubbletea
// runs on its event loop ahead of Update.
func captureTUIFilter(filter func(tea.Model, tea.Msg) tea.Msg) func(tea.Model, tea.Msg) tea.Msg {
	return func(m tea.Model, msg tea.Msg) tea.Msg {
		defer captureTUIPanic()
		return filter(m, msg)
	}
}

func (m panicCapturingModel) Init() tea.Cmd {
	defer captureTUIPanic()
	return captureTUICmd(m.Model.Init())
}

func (m panicCapturingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer captureTUIPanic()
	model, cmd := m.Model.Update(msg)
	return panicCapturingModel{model}, captureTUICmd(cmd)
}

func (m panicCapturingModel) View() tea.View {
	defer captureTUIPanic()
	return m.Model.View()
}

// tuiPanicError is the error the TUI command exits with after bubbletea
// reported a panic. reportsBefore is crash.Written as of the program's
// start, and printed what the stderr tee held: when no report was
// written during the run, the panic bubbletea printed there is saved
// instead. The message names a report only when there is one.
func tuiPanicError(reportsBefore uint64, printed string) error {
	if crash.Written() > reportsBefore {
		return fmt.Errorf("Harness crashed; the panic report with the stack trace is in %s (see `harness crashes`)", crash.Dir()) //nolint:staticcheck
	}
	if value, stack, ok := parseTeaPanic(printed); ok {
		if path := crash.CaptureText("tui", value, stack); path != "" {
			return fmt.Errorf("Harness crashed; the panic report with the stack trace is %s (see `harness crashes`)", path) //nolint:staticcheck
		}
	}
	return fmt.Errorf("Harness crashed and no panic report could be written to %s; the stack trace is above", crash.Dir()) //nolint:staticcheck
}

// Bubbletea's panic print: the value between these two markers, then
// the stack, with every newline written as "\r\n" so it lays out in a
// terminal that may still be in raw mode.
const (
	teaPanicStart = "Caught panic:\r\n\r\n"
	teaPanicEnd   = "\r\n\r\nRestoring terminal...\r\n\r\n"
)

// parseTeaPanic extracts the value and stack of the last panic
// bubbletea printed in out.
func parseTeaPanic(out string) (value, stack string, ok bool) {
	_, after, ok0 := strings.CutLast(out, teaPanicStart)
	if !ok0 {
		return "", "", false
	}
	rest := after
	value, stack, ok = strings.Cut(rest, teaPanicEnd)
	if !ok {
		return "", "", false
	}
	unraw := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	return unraw(value), strings.TrimRight(unraw(stack), "\n"), true
}

// teeStderrLimit is how much of the most recent stderr output the tee
// keeps: a goroutine dump of a busy program runs to tens of kilobytes,
// and the panic print is always the tail.
const teeStderrLimit = 1 << 20

// teeSettle is how long the tee waits for a panic print still being
// written when the program returns: bubbletea signals a panic in one of
// its goroutines before it prints it.
const (
	teeSettle      = time.Second
	teeSettleQuiet = 100 * time.Millisecond
)

// stderrTee passes everything written to os.Stderr through to the real
// stderr and keeps the tail of it while it records.
//
// os.Stderr is replaced once, before the program starts, and never put
// back: bubbletea reads it from goroutines of its own - a command
// goroutine's panic is reported to the program before its stack is
// printed - so restoring the variable when the program returns would
// race with a print still in flight. Stopping only ends the recording;
// the pipe keeps forwarding for the rest of the process.
type stderrTee struct {
	orig *os.File

	mu        sync.Mutex
	recording bool
	buf       []byte
	last      time.Time
}

// teeStderr starts a tee of os.Stderr, or returns nil when it cannot;
// stop on a nil tee is a no-op. Call it before anything that may write
// to os.Stderr from another goroutine has started.
func teeStderr() *stderrTee {
	r, w, err := os.Pipe()
	if err != nil {
		return nil
	}
	t := &stderrTee{orig: os.Stderr, recording: true}
	os.Stderr = w
	go t.copy(r)
	return t
}

func (t *stderrTee) copy(r *os.File) {
	chunk := make([]byte, 32<<10)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			_, _ = t.orig.Write(chunk[:n])
			t.mu.Lock()
			if t.recording {
				t.buf = append(t.buf, chunk[:n]...)
				if over := len(t.buf) - teeStderrLimit; over > 0 {
					t.buf = t.buf[over:]
				}
				t.last = time.Now()
			}
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// stop ends the recording and returns what the tee kept. With panicked
// set it first gives a panic print still in flight a moment to land.
func (t *stderrTee) stop(panicked bool) string {
	if t == nil {
		return ""
	}
	if panicked {
		deadline := time.Now().Add(teeSettle)
		for time.Now().Before(deadline) {
			t.mu.Lock()
			done := strings.Contains(string(t.buf), teaPanicEnd) && time.Since(t.last) >= teeSettleQuiet
			t.mu.Unlock()
			if done {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recording = false
	return string(t.buf)
}
