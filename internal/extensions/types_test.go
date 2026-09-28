package extensions_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/hooks"
	lua "github.com/yuin/gopher-lua"
)

// TestTypeDefinitionsParse checks the stub is valid Lua. The annotations
// are comments, so a language server reads them, but the file still has
// to parse or the server reports nothing useful.
func TestTypeDefinitionsParse(t *testing.T) {
	t.Parallel()

	L := lua.NewState()
	t.Cleanup(L.Close)

	_, err := L.LoadString(extensions.TypeDefinitions)
	require.NoError(t, err)
}

// TestTypeDefinitionsCoverTheAPI guards against the stub drifting from
// the functions actually registered on the harness table.
func TestTypeDefinitionsCoverTheAPI(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "introspect", `
harness.register_tool({
  name = "introspect",
  handler = function()
    local names = {}
    local function walk(prefix, tbl)
      for key, value in pairs(tbl) do
        if type(value) == "function" then
          names[#names + 1] = prefix .. key
        elseif type(value) == "table" then
          walk(prefix .. key .. ".", value)
        end
      end
    end
    walk("harness.", harness)
    table.sort(names)
    return table.concat(names, "\n")
  end,
})
`)

	rsp := runTool(t, newHost(t, []string{root}), "introspect")
	for name := range strings.SplitSeq(rsp.Content, "\n") {
		require.Contains(t, extensions.TypeDefinitions, "function "+name,
			"%s is registered but has no definition in the type stub", name)
	}
}

func TestWriteTypeDefinitions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stub, err := extensions.WriteTypeDefinitions(dir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, extensions.TypesDirName, "harness.lua"), stub)

	written, err := os.ReadFile(stub)
	require.NoError(t, err)
	require.Equal(t, extensions.TypeDefinitions, string(written))

	luarc, err := os.ReadFile(filepath.Join(dir, ".luarc.json"))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(luarc, &settings))
	require.Equal(t, []any{extensions.TypesDirName}, settings["workspace.library"])

	// The types directory holds no init.lua, so discovery ignores it.
	host := newHost(t, []string{dir})
	require.Empty(t, host.Loaded())
}

// TestTypeDefinitionsNameEveryHookEvent keeps the stub's event alias and
// the fields of harness.HookEvent in step with the hook engine: the event
// list is config.HookEvents, and a handler receives the payload encoded
// from hooks.EventContext.
func TestTypeDefinitionsNameEveryHookEvent(t *testing.T) {
	t.Parallel()

	stub := extensions.TypeDefinitions
	var aliased []string
	inAlias := false
	for line := range strings.SplitSeq(stub, "\n") {
		switch {
		case strings.HasPrefix(line, "---@alias harness.HookEventName"):
			inAlias = true
		case inAlias && strings.HasPrefix(line, `---| "`):
			aliased = append(aliased, strings.Trim(strings.TrimPrefix(line, "---| "), `"`))
		case inAlias:
			inAlias = false
		}
	}
	require.Equal(t, config.HookEvents(), aliased)

	var fields []string
	inClass := false
	for line := range strings.SplitSeq(stub, "\n") {
		switch {
		case line == "---@class harness.HookEvent":
			inClass = true
		case inClass && strings.HasPrefix(line, "---@field "):
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "---@field "), " ")
			fields = append(fields, strings.TrimSuffix(name, "?"))
		case inClass:
			inClass = false
		}
	}
	var want []string
	for field := range reflect.TypeFor[hooks.EventContext]().Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			// ToolInput is re-encoded as a table under tool_input.
			name = "tool_input"
		}
		if name != "" {
			want = append(want, name)
		}
	}
	require.ElementsMatch(t, want, fields)
}
