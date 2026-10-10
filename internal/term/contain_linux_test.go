//go:build linux

package term

import (
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

	// tail holds the current line in memory, and /dev/zero has no line
	// breaks: it grows until it is killed.
	require.NoError(t, s.Send([]byte("head -c 1000000000 /dev/zero | tail -n 1; printf '__status:%s__\\n' \"$?\"\n")))
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
