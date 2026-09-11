// Package main is the entry point for the Harness CLI.
//
//	@title			Harness API
//	@version		1.0
//	@description	Harness is a terminal-based AI coding assistant. This API is served over a Unix socket (or Windows named pipe) and provides programmatic access to workspaces, sessions, agents, LSP, MCP, and more.
//	@contact.name	Harness
//	@contact.url	https://github.com/stubbedev/harness
//	@license.name	FSL-1.1-MIT
//	@license.url	https://github.com/stubbedev/harness/blob/main/LICENSE.md
//	@BasePath		/v1
package main

import (
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"

	_ "github.com/joho/godotenv/autoload"
	"github.com/stubbedev/harness/internal/cmd"
	_ "github.com/stubbedev/harness/internal/dns"
)

func main() {
	if os.Getenv("HARNESS_PROFILE") != "" {
		go func() {
			slog.Info("Serving pprof at localhost:6060")
			if httpErr := http.ListenAndServe("localhost:6060", nil); httpErr != nil {
				slog.Error("Failed to pprof listen", "error", httpErr)
			}
		}()
	}

	cmd.Execute()
}
