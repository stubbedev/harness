package extensions_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/hooks"
)

// writeExtension creates an extension directory holding the given
// init.lua and returns the search path it lives in.
func writeExtension(t *testing.T, name, source string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "init.lua"), []byte(source), 0o644))
	return root
}

func newHost(t *testing.T, paths []string, opts ...func(*extensions.Options)) *extensions.Host {
	t.Helper()
	options := extensions.Options{Paths: paths, WorkingDir: t.TempDir()}
	for _, opt := range opts {
		opt(&options)
	}
	host := extensions.New(t.Context(), options)
	t.Cleanup(host.Close)
	return host
}

func TestRegisterTool(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "greeter", `
harness.register_tool({
  name = "greet",
  description = "Greets someone",
  parameters = {
    who = { type = "string", description = "Who to greet", required = true },
  },
  handler = function(input, ctx)
    return "hello " .. input.who .. " from " .. ctx.extension
  end,
})
`)

	host := newHost(t, []string{root})
	require.Equal(t, []string{"greeter"}, host.Loaded())

	tools := host.Tools()
	require.Len(t, tools, 1)

	info := tools[0].Info()
	require.Equal(t, "greet", info.Name)
	require.Equal(t, "Greets someone", info.Description)
	require.Equal(t, []string{"who"}, info.Required)
	require.Contains(t, info.Parameters, "who")

	rsp, err := tools[0].Run(t.Context(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "greet",
		Input: `{"who":"world"}`,
	})
	require.NoError(t, err)
	require.False(t, rsp.IsError)
	require.Equal(t, "hello world from greeter", rsp.Content)
}

func TestToolErrorBecomesToolError(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "boom", `
harness.register_tool({
  name = "boom",
  handler = function() error("kaboom") end,
})
`)

	tools := newHost(t, []string{root}).Tools()
	require.Len(t, tools, 1)

	rsp, err := tools[0].Run(t.Context(), fantasy.ToolCall{ID: "1", Name: "boom", Input: "{}"})
	require.NoError(t, err)
	require.True(t, rsp.IsError)
	require.Contains(t, rsp.Content, "kaboom")
}

func TestToolTableResponse(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "tabled", `
harness.register_tool({
  name = "tabled",
  handler = function()
    return { content = "done", metadata = { count = 2 } }
  end,
})
`)

	tools := newHost(t, []string{root}).Tools()
	rsp, err := tools[0].Run(t.Context(), fantasy.ToolCall{ID: "1", Name: "tabled", Input: "{}"})
	require.NoError(t, err)
	require.Equal(t, "done", rsp.Content)

	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(rsp.Metadata), &metadata))
	require.EqualValues(t, 2, metadata["count"])
}

func TestHookHandlerDenies(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "guard", `
harness.on("PreToolUse", "^shell$", function(event)
  if string.find(event.tool_input.command, "rm %-rf") then
    return { decision = "deny", reason = "no rm -rf here" }
  end
end)
`)

	host := newHost(t, []string{root})
	require.True(t, host.Has(hooks.EventPreToolUse))
	require.False(t, host.Has(hooks.EventStop))

	denied := host.Dispatch(t.Context(), hooks.EventContext{
		Event:     hooks.EventPreToolUse,
		ToolName:  "shell",
		ToolInput: `{"command":"rm -rf /"}`,
	})
	require.Len(t, denied, 1)
	require.Equal(t, hooks.DecisionDeny, denied[0].Result.Decision)
	require.Equal(t, "no rm -rf here", denied[0].Result.Reason)

	allowed := host.Dispatch(t.Context(), hooks.EventContext{
		Event:     hooks.EventPreToolUse,
		ToolName:  "shell",
		ToolInput: `{"command":"ls"}`,
	})
	require.Len(t, allowed, 1)
	require.Equal(t, hooks.DecisionNone, allowed[0].Result.Decision)

	// A matcher that does not match the subject contributes nothing.
	require.Empty(t, host.Dispatch(t.Context(), hooks.EventContext{
		Event:    hooks.EventPreToolUse,
		ToolName: "view",
	}))
}

func TestHookHandlerRewritesPrompt(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "prompter", `
harness.on("UserPromptSubmit", function(event)
  return { updated_prompt = event.prompt .. " (please be brief)" }
end)
`)

	results := newHost(t, []string{root}).Dispatch(t.Context(), hooks.EventContext{
		Event:  hooks.EventUserPromptSubmit,
		Prompt: "explain this",
	})
	require.Len(t, results, 1)
	require.Equal(t, "explain this (please be brief)", results[0].Result.UpdatedPrompt)
}

func TestCommands(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "notes", `
harness.register_command({
  name = "standup",
  description = "Draft a standup note",
  handler = function(args)
    return "Draft a standup for " .. (args.DAY or "today")
  end,
})

harness.register_command({
  name = "static",
  prompt = "Review $FILE for bugs",
  arguments = { { id = "FILE", description = "File to review", required = true } },
})
`)

	host := newHost(t, []string{root})
	cmds := host.Commands()
	require.Len(t, cmds, 2)
	require.Equal(t, "ext:notes:standup", cmds[0].ID)
	require.Equal(t, "Draft a standup note", cmds[0].Description)
	require.Equal(t, "ext:notes:static", cmds[1].ID)
	require.Len(t, cmds[1].Arguments, 1)
	require.True(t, cmds[1].Arguments[0].Required)

	prompt, err := host.RunCommand(t.Context(), "ext:notes:standup", map[string]string{"DAY": "monday"})
	require.NoError(t, err)
	require.Equal(t, "Draft a standup for monday", prompt)

	prompt, err = host.RunCommand(t.Context(), "ext:notes:static", map[string]string{"FILE": "main.go"})
	require.NoError(t, err)
	require.Equal(t, "Review main.go for bugs", prompt)

	_, err = host.RunCommand(t.Context(), "ext:notes:missing", nil)
	require.Error(t, err)
}

func TestSandboxHidesAmbientIO(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "sandboxed", `
harness.register_tool({
  name = "probe",
  handler = function()
    return table.concat({
      tostring(io),
      tostring(debug),
      tostring(os.execute),
      tostring(dofile),
      tostring(loadfile),
    }, ",")
  end,
})
`)

	tools := newHost(t, []string{root}).Tools()
	rsp, err := tools[0].Run(t.Context(), fantasy.ToolCall{ID: "1", Name: "probe", Input: "{}"})
	require.NoError(t, err)
	require.Equal(t, "nil,nil,nil,nil,nil", rsp.Content)
}

func TestLoadFailureIsIsolated(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "broken", `this is not lua`)
	dir := filepath.Join(root, "working")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "init.lua"),
		[]byte(`harness.register_tool({ name = "ok", handler = function() return "ok" end })`),
		0o644,
	))

	host := newHost(t, []string{root})
	require.Equal(t, []string{"working"}, host.Loaded())

	var broken *extensions.State
	for _, state := range host.States() {
		if state.Name == "broken" {
			broken = state
		}
	}
	require.NotNil(t, broken)
	require.Equal(t, extensions.StateError, broken.State)
	require.Error(t, broken.Err)
}

func TestDisabledExtensionIsSkipped(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "noisy", `harness.register_tool({ name = "noise", handler = function() return "" end })`)
	host := newHost(t, []string{root}, func(o *extensions.Options) {
		o.Disabled = []string{"noisy"}
	})
	require.Empty(t, host.Loaded())
	require.Empty(t, host.Tools())
}

func TestRegistrationAfterLoadFails(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "late", `
harness.register_tool({
  name = "late",
  handler = function()
    harness.register_tool({ name = "later", handler = function() return "" end })
    return "unreachable"
  end,
})
`)

	tools := newHost(t, []string{root}).Tools()
	rsp, err := tools[0].Run(t.Context(), fantasy.ToolCall{ID: "1", Name: "late", Input: "{}"})
	require.NoError(t, err)
	require.True(t, rsp.IsError)
	require.Contains(t, rsp.Content, "while the extension loads")
}

func TestNilHostIsInert(t *testing.T) {
	t.Parallel()

	var host *extensions.Host
	require.Nil(t, host.Tools())
	require.Nil(t, host.Commands())
	require.Nil(t, host.States())
	require.Nil(t, host.Loaded())
	require.False(t, host.Has(hooks.EventStop))
	require.Nil(t, host.Dispatch(context.Background(), hooks.EventContext{Event: hooks.EventStop}))
	_, err := host.RunCommand(context.Background(), "ext:x:y", nil)
	require.Error(t, err)
	host.Close()
}
