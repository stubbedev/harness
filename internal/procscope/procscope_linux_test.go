//go:build linux

package procscope

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testPolicy contains the tests' children like Servers does, with a cap
// no user setting can turn off.
var testPolicy = NewPolicy("test", "PROCSCOPE_TEST_UNSET_LIMIT", "50%", "Test children")

// stdioChild is a contained child talked to over pipes, the way a
// language or MCP server is.
type stdioChild struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader
	scope *Scope
}

// startChild starts script under sh in a scope of its own, with FOO set
// in its environment and dir as its working directory. It skips the test
// where there is no systemd user manager to create the scope.
func startChild(t *testing.T, script, dir string) *stdioChild {
	t.Helper()
	name, args, scope := testPolicy.Command("test-"+t.Name(), "sh", []string{"-c", script})
	if scope == nil {
		t.Skip("no systemd user manager to put the child in a scope")
	}
	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Env = append(os.Environ(), "FOO=bar baz")
	cmd.Dir = dir
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		scope.Kill()
		_ = in.Close()
		_ = cmd.Wait()
	})
	return &stdioChild{cmd: cmd, in: in, out: bufio.NewReader(out), scope: scope}
}

func (c *stdioChild) send(t *testing.T, line string) {
	t.Helper()
	_, err := io.WriteString(c.in, line+"\n")
	require.NoError(t, err)
}

func (c *stdioChild) readLine(t *testing.T) string {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		line, _ := c.out.ReadString('\n')
		lines <- strings.TrimSuffix(line, "\n")
	}()
	select {
	case line := <-lines:
		return line
	case <-time.After(30 * time.Second):
		t.Fatal("the child never answered")
		return ""
	}
}

// waitDir waits for the child to have been moved into its scope.
func (c *stdioChild) waitDir(t *testing.T) string {
	t.Helper()
	var dir string
	require.Eventually(t, func() bool {
		dir = CgroupOf(c.cmd.Process.Pid, c.scope.Unit())
		return dir != ""
	}, 5*time.Second, 10*time.Millisecond, "the child never reached its scope")
	return dir
}

// TestCommandRunsInOwnScope checks a wrapped child the way a server is
// run: systemd-run execs it in place, so the pid Harness started is the
// child, in the scope's cgroup, with the stdio, environment and working
// directory Harness gave it.
func TestCommandRunsInOwnScope(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	c := startChild(t, `printf '%s|%s\n' "$FOO" "$PWD"; exec cat`, dir)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.Contains(t, []string{"bar baz|" + dir, "bar baz|" + resolved}, c.readLine(t))

	c.send(t, "hello over stdio")
	require.Equal(t, "hello over stdio", c.readLine(t))

	scopeDir := c.waitDir(t)
	require.Equal(t, scopeDir, c.scope.Dir(), "the scope is found by its unit name alone")
	require.True(t, strings.HasPrefix(c.scope.Unit(), "harness-test-TestCommandRunsInOwnScope-"), c.scope.Unit())
	require.Contains(t, CgroupProcs(scopeDir), c.cmd.Process.Pid)

	own, ok := cgroupPath("/proc/self/cgroup")
	require.True(t, ok)
	require.NotEqual(t, filepath.Join(cgroupRoot, own), scopeDir, "the child shares the test's cgroup")

	limit, ok := c.scope.MemoryMax()
	require.True(t, ok, "a 50%% cap must show in memory.max")
	require.Positive(t, limit)

	c.scope.RaiseOOMScore()
	adj, err := os.ReadFile("/proc/" + strconv.Itoa(c.cmd.Process.Pid) + "/oom_score_adj")
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(OOMScoreAdj), strings.TrimSpace(string(adj)))
}

// TestOOMKillStaysInScope runs a hog inside a child's scope that outgrows
// the scope's memory limit. The kernel kills the hog, and only the hog:
// the child (OOMPolicy=continue) and this test process live on.
func TestOOMKillStaysInScope(t *testing.T) {
	t.Parallel()

	c := startChild(t, `while read -r cmd; do sh -c "$cmd"; echo "status:$?"; done`, t.TempDir())
	dir := c.waitDir(t)
	// A small limit makes the test cheap; the policy's own cap is a share
	// of the machine's memory.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "memory.max"), []byte("64M"), 0o644))

	// tail holds the current line in memory, and /dev/zero has no line
	// breaks: it grows until it is killed.
	c.send(t, "head -c 1000000000 /dev/zero | tail -n 1")
	require.Equal(t, "status:137", c.readLine(t))

	c.send(t, "echo still here")
	require.Equal(t, "still here", c.readLine(t))
	kills, ok := c.scope.OOMKills()
	require.True(t, ok)
	require.Equal(t, 1, kills)
}

// TestKillEndsEscapedProcesses checks that killing a scope ends a
// process that left the child's session with setsid, and that the scope
// is collected once it is empty.
func TestKillEndsEscapedProcesses(t *testing.T) {
	t.Parallel()

	c := startChild(t, `setsid sleep 300 </dev/null >/dev/null 2>&1 & echo started; exec cat`, t.TempDir())
	require.Equal(t, "started", c.readLine(t))
	dir := c.waitDir(t)
	require.Eventually(t, func() bool { return len(CgroupProcs(dir)) >= 2 }, 5*time.Second, 10*time.Millisecond)

	c.scope.Kill()
	require.Eventually(t, func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	}, 5*time.Second, 20*time.Millisecond, "the scope outlived its kill")
}

// TestKillReapsScopeOfChildKilledEarly kills children while systemd-run
// is still registering their scopes, which can leave systemd holding an
// empty scope "running" that --collect never removes. Scope.Kill must
// see every one of them gone.
func TestKillReapsScopeOfChildKilledEarly(t *testing.T) {
	t.Parallel()

	var scopes []*Scope
	for i := range 30 {
		name, args, scope := testPolicy.Command("test-reap", "sleep", []string{"300"})
		if scope == nil {
			t.Skip("no systemd user manager to put the child in a scope")
		}
		cmd := exec.CommandContext(t.Context(), name, args...)
		require.NoError(t, cmd.Start())
		time.Sleep(time.Duration(i) * time.Millisecond)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		scope.Kill()
		scopes = append(scopes, scope)
	}
	require.Eventually(t, func() bool {
		for _, s := range scopes {
			if FindCgroup(s.Unit()) != "" {
				return false
			}
		}
		return true
	}, 10*time.Second, 50*time.Millisecond, "a scope outlived its child")
}

// TestCommandLeavesMissingCommandAlone checks that a command PATH does
// not hold is returned as given, for the caller's usual not-found error.
func TestCommandLeavesMissingCommandAlone(t *testing.T) {
	t.Parallel()

	name, args, scope := testPolicy.Command("missing", "harness-no-such-command", []string{"x"})
	require.Equal(t, "harness-no-such-command", name)
	require.Equal(t, []string{"x"}, args)
	require.Nil(t, scope)
}
