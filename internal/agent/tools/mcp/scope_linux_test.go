//go:build linux

package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/procscope"
)

// fakeServerEnv makes the test binary a stdio MCP server instead of
// running the tests: see TestMain.
const fakeServerEnv = "MCP_TEST_FAKE_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) == "1" {
		fakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// hogHeld keeps what the hog tool allocates reachable.
var hogHeld [][]byte

// fakeServer serves one tool over stdio, hog, which leaves a sleeping
// child behind (so the scope outlives the server and what happened in it
// can still be read) and then allocates memory until the kernel kills
// the server.
func fakeServer() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "hog", Description: "run out of memory"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			_ = exec.CommandContext(context.Background(), "sleep", "300").Start()
			for {
				b := make([]byte, 1<<20)
				for i := range b {
					b[i] = 1
				}
				hogHeld = append(hogHeld, b)
			}
		})
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
}

// startFakeSession connects to the fake server as Harness connects to
// any stdio server, skipping the test where servers cannot be put in
// scopes of their own.
func startFakeSession(t *testing.T) *ClientSession {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	m := config.MCPConfig{
		Type:    config.MCPStdio,
		Command: exe,
		Args:    []string{"-test.run=^$"},
		Env:     map[string]string{fakeServerEnv: "1"},
		Timeout: 30,
	}
	sess, err := createSession(context.Background(), nil, "fake server", m, config.IdentityResolver(), false)
	require.NoError(t, err, "the server never answered over its pipes")
	t.Cleanup(func() { _ = sess.Close() })
	if sess.scope == nil {
		t.Skip("no systemd user manager to put the server in a scope")
	}
	return sess
}

// TestStdioServerRunsInOwnScope checks that a stdio MCP server is
// started in a scope of its own, named for it, and spoken to over its
// own stdio as before; and that closing the session takes the scope
// away.
func TestStdioServerRunsInOwnScope(t *testing.T) {
	t.Parallel()

	sess := startFakeSession(t)
	require.True(t, strings.HasPrefix(sess.scope.Unit(), "harness-mcp-fake_server-"), sess.scope.Unit())
	tools, err := sess.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)

	dir := sess.scope.Dir()
	procs := procscope.CgroupProcs(dir)
	require.Len(t, procs, 1, "the scope holds the server and nothing else")
	adj, err := os.ReadFile(fmt.Sprintf("/proc/%d/oom_score_adj", procs[0]))
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(procscope.OOMScoreAdj), strings.TrimSpace(string(adj)))

	// Close cancels the session first, which kills the server: its error
	// is the kill.
	_ = sess.Close()
	require.Eventually(t, func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	}, 5*time.Second, 20*time.Millisecond, "the scope outlived its session")
}

// TestStdioServerOOMKillStaysInScope runs a stdio MCP server out of its
// scope's memory. The kernel kills the server and nothing else: this
// process lives on, the session's ping fails (which is what puts a
// server in StateError and rebuilds it), and closing the session kills
// what the server left behind.
func TestStdioServerOOMKillStaysInScope(t *testing.T) {
	t.Parallel()

	sess := startFakeSession(t)
	dir := sess.scope.Dir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "memory.max"), []byte("64M"), 0o644))

	_, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "hog"})
	require.Error(t, err, "the hog cannot answer: it is killed")
	kills, ok := sess.scope.OOMKills()
	require.True(t, ok)
	require.Equal(t, 1, kills)
	require.Error(t, pingSession(t.Context(), sess, 5*time.Second))

	require.NotEmpty(t, procscope.CgroupProcs(dir))
	_ = sess.Close()
	require.Eventually(t, func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	}, 5*time.Second, 20*time.Millisecond, "the server's leftovers outlived the session")
}

// TestScopedTransportKeepsConfiguredCommand checks that the command a
// stdio transport describes is the configured server once it has
// started, wrapped or not, so diagnostics re-run the server itself.
func TestScopedTransportKeepsConfiguredCommand(t *testing.T) {
	t.Parallel()

	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), fakeServerEnv+"=1")
	configureStdioProcess(cmd)
	inner := &mcp.CommandTransport{Command: cmd}
	tr := newScopedTransport("fake", inner)
	conn, err := tr.Connect(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
		tr.serverScope().Kill()
	})
	require.Equal(t, exe, cmd.Path)
	require.Equal(t, []string{exe, "-test.run=^$"}, cmd.Args)
	require.Same(t, inner, unwrapTransport(tr))

	// Torn down only once it is in its scope: a child killed while
	// systemd-run is still registering one is Scope.Kill's slow path.
	if scope := tr.serverScope(); scope != nil {
		require.Eventually(t, func() bool {
			return procscope.CgroupOf(cmd.Process.Pid, scope.Unit()) != ""
		}, 5*time.Second, 10*time.Millisecond, "the server never reached its scope")
	}
}
