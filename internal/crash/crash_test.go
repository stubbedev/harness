package crash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func setupTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HARNESS_CRASH_DIR", dir)
	return dir
}

func TestCaptureWritesReport(t *testing.T) {
	setupTestDir(t)

	path := Capture("test/component", "boom")
	require.NotEmpty(t, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	report := string(data)

	require.Contains(t, report, "component: test/component\n")
	require.Contains(t, report, "panic: boom\n")
	require.Contains(t, report, "time: ")
	require.Contains(t, report, "version: ")
	require.Contains(t, report, "stack:\n")
	require.Contains(t, filepath.Base(path), "test-component")
}

func TestCaptureKeepsStackOfPanickingFrames(t *testing.T) {
	setupTestDir(t)

	var path string
	func() {
		defer func() {
			path = Capture("tui", recover())
		}()
		panicProductionLine()
	}()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "panicProductionLine")
}

func panicProductionLine() {
	panic("production line panicked")
}

func TestRecoverCapturesAndRunsCleanup(t *testing.T) {
	dir := setupTestDir(t)

	cleaned := false
	func() {
		defer Recover("guarded", func() { cleaned = true })
		panic("guarded panic")
	}()

	require.True(t, cleaned)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestRecoverWithoutPanicRunsNothing(t *testing.T) {
	setupTestDir(t)

	cleaned := false
	func() {
		defer Recover("quiet", func() { cleaned = true })
	}()
	require.False(t, cleaned)
}

func TestReportPathDodgesCollisions(t *testing.T) {
	dir := setupTestDir(t)

	frozen := time.Date(2026, 9, 22, 14, 30, 5, 123000000, time.UTC)
	first := reportPath(frozen, "dup")
	require.Equal(t, filepath.Join(dir, "20260922-143005.123-dup.log"), first)
	require.NoError(t, os.WriteFile(first, []byte("x"), 0o644))

	second := reportPath(frozen, "dup")
	require.Equal(t, filepath.Join(dir, "20260922-143005.123-2-dup.log"), second)
	require.NotEqual(t, first, second)
}

func TestPruneKeepsNewestReports(t *testing.T) {
	dir := setupTestDir(t)

	// Name the stale files with past timestamps so their names order them
	// before the report Capture is about to write.
	old := time.Now().Add(-time.Hour)
	for i := range maxReports + 5 {
		name := filepath.Join(dir, old.Add(time.Duration(i)*time.Second).Format(reportFilePattern)+"-old.log")
		require.NoError(t, os.WriteFile(name, []byte("x"), 0o644))
		require.NoError(t, os.Chtimes(name, old, old))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("x"), 0o644))

	Capture("fresh", "boom")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	logs := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") {
			logs++
		}
	}
	require.Equal(t, maxReports, logs)
}

func TestGoCapturesPanicAndSurvives(t *testing.T) {
	setupTestDir(t)

	done := Go("pump", func() {
		panic("pump blew up")
	})
	<-done

	reports, err := List()
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, "pump", reports[0].Component)
	require.Equal(t, "pump blew up", reports[0].Panic)
}

func TestGoPassesValueThrough(t *testing.T) {
	setupTestDir(t)

	var got int
	done := Go("calm", func() { got = 42 })
	<-done
	require.Equal(t, 42, got)

	reports, err := List()
	require.NoError(t, err)
	require.Empty(t, reports)
}

func TestListReturnsNewestFirstWithHeaders(t *testing.T) {
	setupTestDir(t)

	older := Capture("older", "first panic")
	time.Sleep(2 * time.Millisecond)
	newer := Capture("newer", "second panic")

	reports, err := List()
	require.NoError(t, err)
	require.Len(t, reports, 2)
	require.Equal(t, filepath.Base(newer), reports[0].Name)
	require.Equal(t, filepath.Base(older), reports[1].Name)
	require.Equal(t, "newer", reports[0].Component)
	require.Equal(t, "second panic", reports[0].Panic)
}
