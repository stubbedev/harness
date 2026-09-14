package extensions

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// TypeDefinitions is the lua-language-server stub describing the harness
// API. It is embedded so `harness extensions types` always emits the
// definitions of the binary in hand, rather than whatever a docs page
// said when it was written.
//
//go:embed types/harness.lua
var TypeDefinitions string

// TypesDirName is the directory the stub is written into, next to the
// extensions it describes. The leading dot keeps it out of discovery:
// only directories holding an init.lua are extensions, but a dotted name
// also reads as "not one of yours".
const TypesDirName = ".types"

// luarcContents is the lua-language-server project file written
// alongside the stub, so an editor opened anywhere under the extensions
// directory picks the definitions up with no further setup.
var luarcContents = mustJSON(map[string]any{
	"$schema":                   "https://raw.githubusercontent.com/LuaLS/vscode-lua/master/setting/schema.json",
	"runtime.version":           "Lua 5.1",
	"workspace.library":         []string{TypesDirName},
	"diagnostics.globals":       []string{"harness"},
	"workspace.checkThirdParty": false,
})

// WriteTypeDefinitions writes the stub and a .luarc.json into dir,
// creating it when absent. It returns the path of the stub.
func WriteTypeDefinitions(dir string) (string, error) {
	typesDir := filepath.Join(dir, TypesDirName)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", typesDir, err)
	}

	stub := filepath.Join(typesDir, "harness.lua")
	if err := os.WriteFile(stub, []byte(TypeDefinitions), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", stub, err)
	}

	luarc := filepath.Join(dir, ".luarc.json")
	if err := os.WriteFile(luarc, luarcContents, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", luarc, err)
	}
	return stub, nil
}

func mustJSON(value any) []byte {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic("extensions: encoding the .luarc.json template: " + err.Error())
	}
	return append(data, '\n')
}
