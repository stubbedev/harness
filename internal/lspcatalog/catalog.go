// Package lspcatalog is the language server catalog Harness bundles:
// powernap's defaults plus the servers Harness adds on top of them.
package lspcatalog

import (
	"fmt"

	powernapconfig "github.com/charmbracelet/x/powernap/pkg/config"
)

// ShellLSP is shell-lsp (github.com/stubbedev/shell-lsp): shell scripts in
// every dialect, and the shell embedded in Makefiles, justfiles and
// Dockerfiles.
const ShellLSP = "shell_lsp"

// supersededBy maps a bundled server to the one that replaces it: wherever
// the successor handles a file and is available, the superseded server is
// not auto-started for it.
var supersededBy = map[string]string{
	"bashls":                 ShellLSP,
	"docker_language_server": ShellLSP,
	"dockerls":               ShellLSP,
	"just":                   ShellLSP,
	"shuck":                  ShellLSP,
}

// additions returns fresh configs for the servers Harness bundles beyond
// powernap's, so no caller can mutate another's copy.
func additions() map[string]*powernapconfig.ServerConfig {
	return map[string]*powernapconfig.ServerConfig{
		ShellLSP: {
			Command: "shell-lsp",
			FileTypes: []string{
				"sh", "bash", "zsh", "ksh", "dash", "bats",
				"makefile", "gnumakefile", "mk", "mak",
				"justfile", "just",
				"dockerfile", "containerfile",
			},
			SingleFileSupport: true,
		},
	}
}

// New returns a catalog the caller owns and may modify.
func New() *powernapconfig.Manager {
	manager := powernapconfig.NewManager()
	if err := manager.LoadDefaults(); err != nil {
		panic(fmt.Sprintf("lspcatalog: load powernap defaults: %v", err))
	}
	for name, server := range additions() {
		manager.AddServer(name, server)
	}
	return manager
}

// SupersededBy returns the server that replaces name, if one does.
func SupersededBy(name string) (string, bool) {
	successor, ok := supersededBy[name]
	return successor, ok
}
