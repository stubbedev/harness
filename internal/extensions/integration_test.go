package extensions_test

import (
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/hooks"
)

// TestHostDispatchesThroughRegistry runs a Lua handler through the real
// hook registry, the way the coordinator wires it, and checks that its
// denial is what the registry reports.
func TestHostDispatchesThroughRegistry(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "guard", `
harness.on("PreToolUse", "^shell$", function(event)
  return { decision = "deny", reason = "lua says no" }
end)
`)

	host := newHost(t, []string{root})
	cfg := &config.Config{}
	require.NoError(t, cfg.ValidateHooks())
	registry := hooks.NewRegistry(config.NewTestStore(cfg), t.TempDir(), t.TempDir()).
		WithDispatchers(host)

	require.True(t, registry.Has(hooks.EventPreToolUse))

	result, err := registry.Run(t.Context(), hooks.EventContext{
		Event:     hooks.EventPreToolUse,
		ToolName:  "shell",
		ToolInput: `{"command":"ls"}`,
	})
	require.NoError(t, err)
	require.Equal(t, hooks.DecisionDeny, result.Decision)
	require.Equal(t, "lua says no", result.Reason)
	require.Equal(t, 1, result.HookCount)
	require.Equal(t, "guard:PreToolUse", result.Hooks[0].Name)
}

// TestConfiguredHookGatesHostFunction checks the other direction: a
// shell hook in the config polices what an extension's host functions
// do, under the synthetic tool names.
func TestConfiguredHookGatesHostFunction(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "writer", `
harness.register_tool({
  name = "writer",
  handler = function()
    harness.fs.write("out.txt", "written")
    return "wrote it"
  end,
})
`)

	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			hooks.EventPreToolUse: {{
				Matcher: "^" + extensions.GateWrite + "$",
				Command: `echo "writes are not allowed" >&2; exit 2`,
			}},
		},
	}
	require.NoError(t, cfg.ValidateHooks())
	workingDir := t.TempDir()
	store := config.NewTestStoreWithWorkingDir(cfg, workingDir)

	host := newHost(t, []string{root}, func(o *extensions.Options) {
		o.WorkingDir = workingDir
		o.Gate = hooks.NewRegistry(store, workingDir, workingDir)
	})

	tools := host.Tools()
	require.Len(t, tools, 1)

	rsp, err := tools[0].Run(t.Context(), fantasy.ToolCall{ID: "1", Name: "writer", Input: "{}"})
	require.NoError(t, err)
	require.True(t, rsp.IsError)
	require.Contains(t, rsp.Content, "writes are not allowed")
	require.NoFileExists(t, workingDir+"/out.txt")
}

// TestOptionsFromStore reads the paths and disabled list off a config
// store the way app.New does.
func TestOptionsFromStore(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "configured", `
harness.register_tool({ name = "configured", handler = function() return "hi" end })
`)

	workingDir := t.TempDir()
	store := config.NewTestStoreWithWorkingDir(&config.Config{
		Options: &config.Options{
			ExtensionsPaths: []string{root},
			DataDirectory:   workingDir,
		},
	}, workingDir)

	opts := extensions.OptionsFromStore(store)
	require.Equal(t, []string{root}, opts.Paths)
	require.Equal(t, workingDir, opts.WorkingDir)
	require.NotNil(t, opts.Gate)

	host := extensions.New(t.Context(), opts)
	t.Cleanup(host.Close)
	require.Equal(t, []string{"configured"}, host.Loaded())
}

// TestDescribeReportsRegistrations covers the diagnostics surface the
// harness self-inspection tool renders.
func TestDescribeReportsRegistrations(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "described", `
harness.register_tool({ name = "described_tool", handler = function() return "" end })
harness.register_command({ name = "described_cmd", prompt = "hello" })
harness.on("Stop", function() end)
`)

	infos := newHost(t, []string{root}).Describe()
	require.Len(t, infos, 1)
	require.Equal(t, "described", infos[0].Name)
	require.Equal(t, "loaded", infos[0].State)
	require.Equal(t, []string{"described_tool"}, infos[0].Tools)
	require.Equal(t, []string{"ext:described:described_cmd"}, infos[0].Commands)
	require.Equal(t, []string{hooks.EventStop}, infos[0].Events)
}

// TestDocumentedExamplesLoad keeps the examples in docs/extensions from
// rotting: they are loaded exactly as a user's would be, and what they
// register is what the documentation claims.
func TestDocumentedExamplesLoad(t *testing.T) {
	t.Parallel()

	host := newHost(t, []string{filepath.Join("..", "..", "docs", "extensions", "examples")})
	require.Equal(t, []string{"link-check", "no-force-push", "todos"}, host.Loaded())

	var names []string
	for _, tool := range host.Tools() {
		names = append(names, tool.Info().Name)
	}
	require.Equal(t, []string{"extension_jobs", "link_check", "todo_list"}, names)

	commands := host.Commands()
	require.Len(t, commands, 1)
	require.Equal(t, "ext:todos:triage", commands[0].ID)

	denied := host.Dispatch(t.Context(), hooks.EventContext{
		Event:     hooks.EventPreToolUse,
		ToolName:  "shell",
		ToolInput: `{"command":"git push --force origin main"}`,
	})
	require.Len(t, denied, 1)
	require.Equal(t, hooks.DecisionDeny, denied[0].Result.Decision)

	allowed := host.Dispatch(t.Context(), hooks.EventContext{
		Event:     hooks.EventPreToolUse,
		ToolName:  "shell",
		ToolInput: `{"command":"git push --force-with-lease origin main"}`,
	})
	require.Len(t, allowed, 1)
	require.Equal(t, hooks.DecisionNone, allowed[0].Result.Decision)
}
