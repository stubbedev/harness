package extensions_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/hooks"
)

func TestAsyncRunsInParallel(t *testing.T) {
	t.Parallel()

	// The server holds each request for 250ms. Three requests serially
	// would take 750ms; overlapping they take a little over 250ms.
	const hold = 250 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(hold)
		_, _ = w.Write([]byte(strings.TrimPrefix(r.URL.Path, "/")))
	}))
	t.Cleanup(server.Close)

	root := writeExtension(t, "parallel", `
harness.register_tool({
  name = "parallel",
  handler = function()
    local handles = {}
    for i = 1, 3 do
      handles[i] = harness.http.request({ url = URL .. "/" .. i, async = true })
    end
    local a, b, c = harness.await(handles[1], handles[2], handles[3])
    return a.body .. b.body .. c.body
  end,
})
`)
	prependGlobal(t, root, "parallel", "URL", server.URL)

	started := time.Now()
	rsp := runTool(t, newHost(t, []string{root}, func(o *extensions.Options) {
		o.HTTPClient = server.Client()
	}), "parallel")
	elapsed := time.Since(started)

	require.Equal(t, "123", rsp.Content)
	require.GreaterOrEqual(t, elapsed, hold, "the requests did not actually run")
	require.Less(t, elapsed, 3*hold, "three %s requests took %s; they did not overlap", hold, elapsed)
}

func TestAsyncHTTPAndHandleMethod(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello " + strings.TrimPrefix(r.URL.Path, "/")))
	}))
	t.Cleanup(server.Close)

	root := writeExtension(t, "asynchttp", `
harness.register_tool({
  name = "asynchttp",
  handler = function()
    local first = harness.http.request({ url = URL .. "/one", async = true })
    local second = harness.http.request({ url = URL .. "/two", async = true })
    -- Both forms: the method on the handle, and the free function.
    local a = first:await()
    local b = harness.await(second)
    return a.body .. "|" .. b.body .. "|" .. tostring(a.status)
  end,
})
`)
	prependGlobal(t, root, "asynchttp", "URL", server.URL)

	rsp := runTool(t, newHost(t, []string{root}, func(o *extensions.Options) {
		o.HTTPClient = server.Client()
	}), "asynchttp")
	require.Equal(t, "hello one|hello two|200", rsp.Content)
}

func TestAsyncHTTPErrorRidesInTheResult(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "asyncerr", `
harness.register_tool({
  name = "asyncerr",
  handler = function()
    local handle = harness.http.request({ url = "http://127.0.0.1:1/nope", async = true })
    local result = harness.await(handle)
    return tostring(result.ok) .. "|" .. (result.error ~= nil and "has error" or "no error")
  end,
})
`)

	rsp := runTool(t, newHost(t, []string{root}), "asyncerr")
	require.Equal(t, "false|has error", rsp.Content)
}

func TestAsyncGateAppliesAtStart(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "asyncgated", `
harness.register_tool({
  name = "asyncgated",
  handler = function()
    harness.exec("echo nope", { async = true })
    return "unreachable"
  end,
})
`)

	gate := &fakeGate{decision: hooks.DecisionDeny, reason: "no background work either"}
	rsp := runTool(t, newHost(t, []string{root}, func(o *extensions.Options) { o.Gate = gate }), "asyncgated")

	require.True(t, rsp.IsError)
	require.Contains(t, rsp.Content, "no background work either")
	require.Equal(t, []string{extensions.GateExec}, gate.events)
}

func TestAwaitRejectsNonHandles(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "badawait", `
harness.register_tool({
  name = "badawait",
  handler = function() return harness.await("not a handle") end,
})
`)

	rsp := runTool(t, newHost(t, []string{root}), "badawait")
	require.True(t, rsp.IsError)
	require.Contains(t, rsp.Content, "expected a handle")
}

// prependGlobal writes a global assignment above an extension's
// init.lua, so a test server's address can reach the script without
// templating the file.
func prependGlobal(t *testing.T, root, name, global, value string) {
	t.Helper()
	path := filepath.Join(root, name, "init.lua")
	source := mustRead(t, path)
	require.NoError(t, os.WriteFile(
		path,
		[]byte(global+" = "+strconv(value)+"\n"+source),
		0o644,
	))
}

func strconv(value string) string { return `"` + value + `"` }
