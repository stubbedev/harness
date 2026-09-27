package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agentstate"
)

// recordingSender captures state transitions without connecting to a
// real Unix socket.
type recordingSender struct {
	states []string
}

func (r *recordingSender) send(req reportRequest) error {
	r.states = append(r.states, req.Params.State)
	return nil
}

func (r *recordingSender) close() {}

// newTestClient creates a Client that records state transitions
// without connecting to a real Unix socket.
func newTestClient() *Client {
	rec := &recordingSender{states: make([]string, 0, 16)}
	return newClient("", "", 0, rec)
}

// reportedStates returns the states recorded by the test sender.
func reportedStates(c *Client) []string {
	return c.snd.(*recordingSender).states
}

func TestBasicLifecycle(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// Assistant message starts working.
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "sess-1"})
	assert.Equal(t, []string{stateWorking}, reportedStates(c))

	// Run complete returns to idle.
	c.HandleEvent(agentstate.RunComplete{SessionID: "sess-1"})
	assert.Equal(t, []string{stateWorking, stateIdle}, reportedStates(c))
}

func TestSessionIDPropagation(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// SetSessionID before events.
	c.SetSessionID("early-session")
	assert.Equal(t, "early-session", c.sessionID)

	// RunComplete also updates session ID.
	c.HandleEvent(agentstate.RunComplete{SessionID: "final-session"})
	assert.Equal(t, "final-session", c.sessionID)
}

func TestDedupSkipsRedundantState(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// Two assistant messages in a row should only report working once.
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})
	assert.Equal(t, []string{stateWorking}, reportedStates(c))
}

func TestSummarizingTriggersWorking(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// Summarizing event should trigger working.
	c.HandleEvent(agentstate.Summarizing{})
	assert.Equal(t, []string{stateWorking}, reportedStates(c))

	// Second summarizing should not trigger another state change.
	c.HandleEvent(agentstate.Summarizing{})
	assert.Equal(t, []string{stateWorking}, reportedStates(c))
}

func TestFailedRunReportsIdle(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// herdr has no error state: a failed run is waiting for input again.
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})
	c.HandleEvent(agentstate.RunComplete{SessionID: "s1", Error: "boom"})
	c.HandleEvent(agentstate.RunComplete{SessionID: "s1"})
	assert.Equal(t, []string{stateWorking, stateIdle}, reportedStates(c))
}

func TestNilClientSafe(t *testing.T) {
	t.Parallel()
	var c *Client
	// These should not panic on a nil receiver.
	c.SetSessionID("s1")
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})
	c.HandleEvent(agentstate.RunComplete{SessionID: "s1"})
	c.HandleEvent(agentstate.Summarizing{})
}

func TestRegisterInitial(t *testing.T) {
	t.Parallel()
	rec := &recordingSender{states: make([]string, 0, 16)}
	c := newClient("", "", 100, rec)
	c.registerInitial()
	assert.Equal(t, []string{stateIdle}, rec.states)
	// seq must strictly increase so herdr accepts the report.
	assert.Equal(t, uint64(101), c.seq)
}

// TestInitDisabledUnderTest guards the critical safety property that
// herdr never attaches to a real pane from a test binary. Test
// processes inherit the developer's HERDR_* environment, so a missing
// guard would release the live pane's agent on teardown. Because this
// test itself runs under `go test`, Init must return nil even with a
// complete, valid-looking environment.
func TestInitDisabledUnderTest(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/does-not-matter.sock")
	t.Setenv("HERDR_PANE_ID", "test:pane")
	assert.Nil(t, newFromEnv())
}

// herdrTestServer is a stand-in for herdr's socket API. It counts
// every connection it accepts and records the reports it receives, so
// tests can tell connection reuse from repeated dialing.
type herdrTestServer struct {
	path string

	mu       sync.Mutex
	listener net.Listener
	conns    []net.Conn
	received []reportRequest
}

// startHerdrServer listens on a Unix socket in a temporary directory
// and serves until the test ends.
func startHerdrServer(t *testing.T) *herdrTestServer {
	t.Helper()
	s := &herdrTestServer{path: filepath.Join(t.TempDir(), "herdr.sock")}
	s.listen()
	t.Cleanup(s.stop)
	return s
}

// listen binds the socket and starts accepting connections.
func (s *herdrTestServer) listen() {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", s.path)
	if err != nil {
		panic(err) // Only reachable before startHerdrServer returns.
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	go s.serve(ln)
}

func (s *herdrTestServer) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		go s.handle(conn)
	}
}

// handle records one report per line for the life of the connection.
// Like herdr, the server keeps the connection open, which is what lets
// the client reuse it across reports.
func (s *herdrTestServer) handle(conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var req reportRequest
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		s.mu.Lock()
		s.received = append(s.received, req)
		s.mu.Unlock()
	}
	// Read errors mean the connection went away, which is exactly
	// what the restart test simulates; nothing to record.
	_ = scanner.Err()
}

// stop closes the listener and every accepted connection, simulating
// the herdr server disappearing entirely.
func (s *herdrTestServer) stop() {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	conns := slices.Clone(s.conns)
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// restart brings a stopped server back on the same socket path.
func (s *herdrTestServer) restart() {
	// A closed Unix listener leaves its socket file behind.
	_ = os.Remove(s.path)
	s.listen()
}

// connectionCount returns how many connections the server has ever
// accepted.
func (s *herdrTestServer) connectionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// reports returns the reports received so far.
func (s *herdrTestServer) reports() []reportRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.received)
}

// reportStates returns the state carried by each received report.
func reportStates(reports []reportRequest) []string {
	states := make([]string, 0, len(reports))
	for _, req := range reports {
		states = append(states, req.Params.State)
	}
	return states
}

// TestUnixSenderReusesConnection proves the sender dials once and
// rides that connection across several reports.
func TestUnixSenderReusesConnection(t *testing.T) {
	t.Parallel()
	srv := startHerdrServer(t)
	c := newClient(srv.path, "test:pane", 0, newUnixSender(srv.path))
	defer c.Close()

	// Four reports: init idle, working, idle, working.
	c.registerInitial()
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})
	c.HandleEvent(agentstate.RunComplete{SessionID: "s1"})
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})

	want := []string{stateIdle, stateWorking, stateIdle, stateWorking}
	require.Eventually(t, func() bool {
		return len(srv.reports()) == len(want)
	}, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, want, reportStates(srv.reports()))
	assert.Equal(t, 1, srv.connectionCount())
}

// TestUnixSenderRecoversAfterServerRestart proves a failed write does
// not wedge the sender: the affected report falls back to a dial-once
// attempt against the downed server and is dropped, no error reaches
// the caller, and the long-lived connection is re-established once the
// server returns.
func TestUnixSenderRecoversAfterServerRestart(t *testing.T) {
	t.Parallel()
	srv := startHerdrServer(t)
	c := newClient(srv.path, "test:pane", 0, newUnixSender(srv.path))
	defer c.Close()

	c.registerInitial()
	require.Eventually(t, func() bool {
		return len(srv.reports()) == 1
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, srv.connectionCount())

	// The second report rides the same connection.
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})
	require.Eventually(t, func() bool {
		return len(srv.reports()) == 2
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, srv.connectionCount())

	// The herdr server disappears: listener and live connections
	// all close.
	srv.stop()

	// The next report hits the dead connection. Its write fails and
	// the dial-once fallback fails too, so it is dropped. Give the
	// writer loop time to notice before the server returns, so the
	// fallback cannot accidentally reach the restarted listener.
	c.HandleEvent(agentstate.RunComplete{SessionID: "s1"})
	time.Sleep(250 * time.Millisecond)
	assert.Equal(t, 1, srv.connectionCount())
	assert.Len(t, srv.reports(), 2)

	// The server returns; the next report re-dials and is delivered
	// over a fresh connection.
	srv.restart()
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})
	require.Eventually(t, func() bool {
		return len(srv.reports()) == 3
	}, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, []string{stateIdle, stateWorking, stateWorking}, reportStates(srv.reports()))
	assert.Equal(t, 2, srv.connectionCount())
}
