package extensions_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/extensions"
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
