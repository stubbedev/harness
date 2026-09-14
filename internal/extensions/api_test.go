package extensions_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/hooks"
)

// fakeGate answers every gated call with a fixed decision, recording
// what it was asked about.
type fakeGate struct {
	events   []string
	decision hooks.Decision
	reason   string
}

func (g *fakeGate) Has(event string) bool { return event == hooks.EventPreToolUse }

func (g *fakeGate) Run(_ context.Context, ec hooks.EventContext) (hooks.AggregateResult, error) {
	g.events = append(g.events, ec.ToolName)
	return hooks.AggregateResult{Decision: g.decision, Reason: g.reason}, nil
}

func runTool(t *testing.T, host *extensions.Host, name string) fantasy.ToolResponse {
	t.Helper()
	for _, tool := range host.Tools() {
		if tool.Info().Name != name {
			continue
		}
		rsp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "1", Name: name, Input: "{}"})
		require.NoError(t, err)
		return rsp
	}
	t.Fatalf("tool %q was not registered", name)
	return fantasy.ToolResponse{}
}

func TestFilesystemRoundTrip(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "files", `
harness.register_tool({
  name = "files",
  handler = function()
    harness.fs.write("notes/todo.txt", "first\n")
    harness.fs.append("notes/todo.txt", "second\n")
    local entries = harness.fs.list("notes")
    return harness.fs.read("notes/todo.txt") .. "#" .. entries[1].name ..
      "#" .. tostring(harness.fs.exists("notes/todo.txt"))
  end,
})
`)

	workdir := t.TempDir()
	host := newHost(t, []string{root}, func(o *extensions.Options) { o.WorkingDir = workdir })

	rsp := runTool(t, host, "files")
	require.Equal(t, "first\nsecond\n#todo.txt#true", rsp.Content)

	written, err := os.ReadFile(filepath.Join(workdir, "notes", "todo.txt"))
	require.NoError(t, err)
	require.Equal(t, "first\nsecond\n", string(written))
}

func TestExecRunsThroughTheEmbeddedShell(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "runner", `
harness.register_tool({
  name = "runner",
  handler = function()
    local result = harness.exec("echo hi && exit 3")
    return result.stdout .. "#" .. tostring(result.code) .. "#" .. tostring(result.ok)
  end,
})
`)

	rsp := runTool(t, newHost(t, []string{root}), "runner")
	require.Equal(t, "hi\n#3#false", rsp.Content)
}

func TestHTTPRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"method":"` + r.Method + `","token":"` + r.Header.Get("X-Token") + `"}`))
	}))
	t.Cleanup(server.Close)

	root := writeExtension(t, "fetcher", `
harness.register_tool({
  name = "fetcher",
  handler = function()
    local rsp = harness.http.get(URL, { ["X-Token"] = "abc" })
    local body = harness.json.decode(rsp.body)
    return tostring(rsp.status) .. "#" .. body.method .. "#" .. body.token
  end,
})
`)
	// The URL is injected as a global so the test server's address does
	// not have to be templated into the script.
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "fetcher", "init.lua"),
		[]byte("URL = \""+server.URL+"\"\n"+mustRead(t, filepath.Join(root, "fetcher", "init.lua"))),
		0o644,
	))

	rsp := runTool(t, newHost(t, []string{root}, func(o *extensions.Options) {
		o.HTTPClient = server.Client()
	}), "fetcher")
	require.Equal(t, "200#GET#abc", rsp.Content)
}

func TestGateDeniesPrivilegedCalls(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "gated", `
harness.register_tool({
  name = "gated",
  handler = function()
    harness.exec("echo nope")
    return "unreachable"
  end,
})
`)

	gate := &fakeGate{decision: hooks.DecisionDeny, reason: "not allowed"}
	rsp := runTool(t, newHost(t, []string{root}, func(o *extensions.Options) { o.Gate = gate }), "gated")

	require.True(t, rsp.IsError)
	require.Contains(t, rsp.Content, "not allowed")
	require.Equal(t, []string{extensions.GateExec}, gate.events)
}

func TestGateAllowsPrivilegedCalls(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "allowed", `
harness.register_tool({
  name = "allowed",
  handler = function()
    return harness.exec("echo yes").stdout
  end,
})
`)

	gate := &fakeGate{decision: hooks.DecisionAllow}
	rsp := runTool(t, newHost(t, []string{root}, func(o *extensions.Options) { o.Gate = gate }), "allowed")

	require.False(t, rsp.IsError)
	require.Equal(t, "yes\n", rsp.Content)
	require.Equal(t, []string{extensions.GateExec}, gate.events)
}

func TestRequireResolvesInsideTheExtension(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "modular", `
local helper = require("helper")
harness.register_tool({
  name = "modular",
  handler = function() return helper.greeting() end,
})
`)
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "modular", "helper.lua"),
		[]byte(`return { greeting = function() return "from a helper" end }`),
		0o644,
	))

	rsp := runTool(t, newHost(t, []string{root}), "modular")
	require.Equal(t, "from a helper", rsp.Content)
}

func TestWorkspaceAndEnvHelpers(t *testing.T) {
	t.Setenv("HARNESS_EXTENSION_TEST", "set")

	root := writeExtension(t, "env", `
harness.register_tool({
  name = "env",
  handler = function()
    local ws = harness.workspace()
    return harness.env("HARNESS_EXTENSION_TEST") .. "#" .. ws.root .. "#" ..
      harness.name .. "#" .. tostring(harness.env("HARNESS_EXTENSION_ABSENT"))
  end,
})
`)

	workdir := t.TempDir()
	host := newHost(t, []string{root}, func(o *extensions.Options) { o.WorkingDir = workdir })
	rsp := runTool(t, host, "env")
	require.Equal(t, "set#"+workdir+"#env#nil", rsp.Content)
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
