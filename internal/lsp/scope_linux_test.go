//go:build linux

package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/procscope"
)

// fakeServerEnv makes the test binary a language server instead of
// running the tests: see TestMain.
const fakeServerEnv = "LSP_TEST_FAKE_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) == "1" {
		fakeServer(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeServer is the least of a language server: it answers initialize
// and shutdown, exits on exit, and on workspace/didChangeWatchedFiles
// allocates memory until the kernel kills it.
func fakeServer(in io.Reader, out io.Writer) {
	r := bufio.NewReader(in)
	for {
		length := 0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if v, ok := strings.CutPrefix(line, "Content-Length:"); ok {
				length, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		switch msg.Method {
		case "exit":
			return
		case "workspace/didChangeWatchedFiles":
			hog()
		}
		if len(msg.ID) == 0 {
			continue
		}
		var result any
		if msg.Method == "initialize" {
			result = map[string]any{"capabilities": map[string]any{}}
		}
		reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
		fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(reply), reply)
	}
}

// hogHeld keeps what hog allocates reachable.
var hogHeld [][]byte

// hog starts a child that sleeps, so the scope outlives the server and
// what happened in it can still be read, then allocates and touches
// memory forever.
func hog() {
	_ = exec.CommandContext(context.Background(), "sleep", "300").Start()
	for {
		b := make([]byte, 1<<20)
		for i := range b {
			b[i] = 1
		}
		hogHeld = append(hogHeld, b)
	}
}

// startFakeClient starts the fake server behind a client, skipping the
// test where servers cannot be put in scopes of their own.
func startFakeClient(t *testing.T) *Client {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	cfg := config.LSPConfig{
		Command: exe,
		Args:    []string{"-test.run=^$"},
		Env:     map[string]string{fakeServerEnv: "1"},
	}
	c, err := New("fake server", cfg, config.IdentityResolver(), t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(c.Shutdown)
	if c.scope.Load() == nil {
		t.Skip("no systemd user manager to put the server in a scope")
	}
	_, err = c.Initialize(t.Context(), c.cwd)
	require.NoError(t, err, "the server never answered over its pipes")
	c.SetServerState(StateReady)
	return c
}

// TestServerRunsInOwnScope checks that a language server is started in a
// scope of its own, named for it, and spoken to over its own stdio as
// before; and that shutting the client down takes the scope away.
func TestServerRunsInOwnScope(t *testing.T) {
	t.Parallel()

	c := startFakeClient(t)
	scope := c.scope.Load()
	require.True(t, strings.HasPrefix(scope.Unit(), "harness-lsp-fake_server-"), scope.Unit())
	require.Equal(t, scope.Unit(), filepath.Base(scope.Dir()))
	procs := procscope.CgroupProcs(scope.Dir())
	require.Len(t, procs, 1, "the scope holds the server and nothing else")
	adj, err := os.ReadFile(fmt.Sprintf("/proc/%d/oom_score_adj", procs[0]))
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(procscope.OOMScoreAdj), strings.TrimSpace(string(adj)))
	require.Equal(t, scope.Unit(), filepath.Base(procscope.CgroupOf(procs[0], scope.Unit())))
	require.Equal(t, c.config.Command, c.pn().Name, "powernap must see the server's name, not systemd-run's")

	dir := scope.Dir()
	c.Shutdown()
	require.Eventually(t, func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	}, 5*time.Second, 20*time.Millisecond, "the scope outlived its server")
}

// TestServerOOMKillStaysInScope runs a language server out of its
// scope's memory. The kernel kills the server and nothing else: this
// process lives on, the client goes to StateError and says so, and the
// manager's next start of the server would replace it.
func TestServerOOMKillStaysInScope(t *testing.T) {
	t.Parallel()

	c := startFakeClient(t)
	var dead atomic.Int32
	c.onDead = func() { dead.Add(1) }
	scope := c.scope.Load()
	require.NoError(t, os.WriteFile(filepath.Join(scope.Dir(), "memory.max"), []byte("64M"), 0o644))

	require.NoError(t, c.NotifyWorkspaceChange(t.Context()))
	require.Eventually(t, func() bool {
		kills, ok := scope.OOMKills()
		return ok && kills == 1
	}, 30*time.Second, 20*time.Millisecond, "the server was never killed")

	// The next message to the server finds it gone.
	require.Eventually(t, func() bool {
		_ = c.NotifyWorkspaceChange(t.Context())
		return c.GetServerState() == StateError
	}, 10*time.Second, 20*time.Millisecond, "the client never noticed its server died")
	require.Equal(t, int32(1), dead.Load(), "observers are told once")
	require.False(t, c.GetServerState().isLive(), "a dead server must be startable again")

	// What the server started outlives it in the scope until the client
	// is shut down, which kills the scope as a whole.
	dir := scope.Dir()
	require.NotEmpty(t, procscope.CgroupProcs(dir))
	c.Shutdown()
	require.Eventually(t, func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	}, 5*time.Second, 20*time.Millisecond, "the server's leftovers outlived the client")
}
