//go:build linux

package term

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSession_OOMKillStaysInScope runs a command that outgrows the
// session's memory limit. The kernel has to kill that command and only
// that command: the shell lives on, and the kill is counted where the
// shell tool can report it.
func TestSession_OOMKillStaysInScope(t *testing.T) {
	s := startTestSession(t)
	if s.unit == "" {
		t.Skip("no systemd user manager to put the session in a scope")
	}
	waitReady(t, s)

	dir := s.scopeDir()
	require.NotEmpty(t, dir, "the session's scope cgroup was never found")
	// A small limit makes the test cheap; the scope's own default is a
	// share of the machine's memory.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "memory.max"), []byte("64M"), 0o644))

	// The hog is this test binary rerun as TestMemoryHog, which allocates
	// until it is killed. It is the only process allocating, so nothing
	// else hits the limit before the kernel has reclaimed its memory. A
	// coreutils trick such as `tail -n 1 /dev/zero` is not a hog
	// everywhere: uutils tail streams the zeros instead of holding the
	// line.
	hog := fmt.Sprintf("%s=1 '%s' -test.run='^TestMemoryHog$'; printf '__status:%%s__\\n' \"$?\"\n", memoryHogEnv, os.Args[0])
	require.NoError(t, s.Send([]byte(hog)))
	require.True(t, waitOutput(t, s, regexp.MustCompile(`__status:137__`), testTimeout(30*time.Second)),
		"the hog was not killed: %q", s.Pending())

	require.True(t, s.Alive(), "the shell died with the command")
	kills, ok := s.OOMKills()
	require.True(t, ok)
	require.Equal(t, 1, kills)
	limit, ok := s.MemoryLimit()
	require.True(t, ok)
	require.Equal(t, int64(64<<20), limit)
}

const memoryHogEnv = "HARNESS_TEST_MEMORY_HOG"

// memoryHogHeld keeps every chunk TestMemoryHog allocates reachable, so
// none of it can be collected.
var memoryHogHeld [][]byte

// TestMemoryHog is the helper process TestSession_OOMKillStaysInScope
// runs inside the session's scope. It touches every page it allocates,
// so its resident memory grows until the kernel kills it.
func TestMemoryHog(t *testing.T) {
	if os.Getenv(memoryHogEnv) == "" {
		t.Skip("helper process for TestSession_OOMKillStaysInScope")
	}
	for {
		chunk := make([]byte, 1<<20)
		for i := 0; i < len(chunk); i += os.Getpagesize() {
			chunk[i] = 1
		}
		memoryHogHeld = append(memoryHogHeld, chunk)
	}
}

// TestSession_CloseKillsScope checks that closing a session ends a
// process that left the shell's session with setsid: the cgroup holds it
// regardless.
func TestSession_CloseKillsScope(t *testing.T) {
	s := startTestSession(t)
	if s.unit == "" {
		t.Skip("no systemd user manager to put the session in a scope")
	}
	waitReady(t, s)
	dir := s.scopeDir()
	require.NotEmpty(t, dir)

	require.NoError(t, s.Send([]byte("setsid sleep 300 </dev/null >/dev/null 2>&1 &\n")))
	require.Eventually(t, func() bool {
		procs, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
		return err == nil && len(regexp.MustCompile(`\d+`).FindAll(procs, -1)) >= 2
	}, testTimeout(5*time.Second), 20*time.Millisecond)

	s.Close()
	require.Eventually(t, func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	}, testTimeout(5*time.Second), 20*time.Millisecond, "the scope outlived its session")
}
