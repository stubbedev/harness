package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/crash"
)

// crashDir points crash reports at a fresh directory that does not exist
// yet, as on a machine that has never crashed.
func crashDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "crashes")
	t.Setenv("HARNESS_CRASH_DIR", dir)
	return dir
}

func reports(t *testing.T) []crash.Report {
	t.Helper()
	list, err := crash.List()
	require.NoError(t, err)
	return list
}

func panickingCmd() tea.Msg {
	panic("inner command exploded")
}

// runWrapped runs cmd as bubbletea would - recovering the panic its
// goroutine raises - and returns what cmd returned.
func runWrapped(cmd tea.Cmd) (msg tea.Msg) {
	defer func() { _ = recover() }()
	return cmd()
}

func TestCaptureTUICmd_BatchedCommandPanicIsCaptured(t *testing.T) {
	crashDir(t)

	msg := runWrapped(captureTUICmd(tea.Batch(panickingCmd, func() tea.Msg { return nil })))
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok)
	for _, inner := range batch {
		runWrapped(inner)
	}

	list := reports(t)
	require.Len(t, list, 1)
	require.Equal(t, "inner command exploded", list[0].Panic)
}

func TestCaptureTUICmd_SequencedCommandPanicIsCaptured(t *testing.T) {
	crashDir(t)

	// tea.Sequence's message type is unexported; it is still a list of
	// commands, and each is wrapped in place.
	msg := runWrapped(captureTUICmd(tea.Sequence(func() tea.Msg { return nil }, panickingCmd)))
	v := reflect.ValueOf(msg)
	require.Equal(t, reflect.Slice, v.Kind())
	for i := range v.Len() {
		c, _ := reflect.TypeAssert[tea.Cmd](v.Index(i))
		runWrapped(c)
	}
	require.Len(t, reports(t), 1)
}

func TestCaptureTUIFilter_PanicIsCaptured(t *testing.T) {
	crashDir(t)

	filter := captureTUIFilter(func(tea.Model, tea.Msg) tea.Msg { panic("filter exploded") })
	require.Panics(t, func() { filter(nil, nil) })
	require.Len(t, reports(t), 1)
}

func TestParseTeaPanic(t *testing.T) {
	t.Parallel()

	out := "noise\r\n" + teaPanicStart + "runtime error: index out of range [3] with length 3" +
		teaPanicEnd + "goroutine 7 [running]:\r\nmain.boom()\r\n\t/x/main.go:9\r\n"
	value, stack, ok := parseTeaPanic(out)
	require.True(t, ok)
	require.Equal(t, "runtime error: index out of range [3] with length 3", value)
	require.Equal(t, "goroutine 7 [running]:\nmain.boom()\n\t/x/main.go:9", stack)

	_, _, ok = parseTeaPanic("no panic here")
	require.False(t, ok)
}

func TestTUIPanicError_SavesPrintedPanicWhenNothingWasCaptured(t *testing.T) {
	crashDir(t)

	printed := teaPanicStart + "renderer exploded" + teaPanicEnd + "goroutine 1 [running]:\r\nuv.render()\r\n"
	err := tuiPanicError(crash.Written(), printed)

	list := reports(t)
	require.Len(t, list, 1)
	require.Equal(t, "renderer exploded", list[0].Panic)
	require.Contains(t, err.Error(), list[0].Path)
}

func TestTUIPanicError_SaysSoWhenThereIsNoReport(t *testing.T) {
	crashDir(t)

	err := tuiPanicError(crash.Written(), "")
	require.Contains(t, err.Error(), "no panic report could be written")
	_, statErr := os.Stat(crash.Dir())
	require.True(t, errors.Is(statErr, os.ErrNotExist), "no report, no directory")
}

// batchPanicModel's first command panics inside a tea.Batch, in one of
// bubbletea's own goroutines: the path that used to leave no report.
type batchPanicModel struct{}

func (batchPanicModel) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return nil }, panickingCmd)
}
func (m batchPanicModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (batchPanicModel) View() tea.View                        { return tea.NewView("") }

func TestTUIPanicInBatchedCommandLeavesAReport(t *testing.T) {
	crashDir(t)

	reportsBefore := crash.Written()
	tee := teeStderr()
	program := tea.NewProgram(
		newPanicCapturingModel(batchPanicModel{}),
		tea.WithInput(nil),
		tea.WithOutput(&bytes.Buffer{}),
		tea.WithContext(t.Context()),
	)
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		program.Kill()
		t.Fatal("program did not stop after the panic")
	}
	printed := tee.stop(errors.Is(err, tea.ErrProgramPanic))
	require.ErrorIs(t, err, tea.ErrProgramPanic)
	require.Contains(t, printed, "inner command exploded", "bubbletea's print passes through the tee")

	msg := tuiPanicError(reportsBefore, printed).Error()
	list := reports(t)
	require.Len(t, list, 1, "exactly one report: the wrapper's, not a second one from the print")
	require.Equal(t, "inner command exploded", list[0].Panic)
	require.Contains(t, msg, crash.Dir())
	body, readErr := os.ReadFile(list[0].Path)
	require.NoError(t, readErr)
	require.True(t, strings.Contains(string(body), "panickingCmd"), fmt.Sprintf("the stack has the panicking frame:\n%s", body))
}
