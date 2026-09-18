// Package home provides utilities for dealing with the user's home directory.
package home

import (
	"cmp"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

var homedir, homedirErr = os.UserHomeDir()

func init() {
	if homedirErr != nil {
		slog.Error("Failed to get user home directory", "error", homedirErr)
	}
}

// Dir returns the user home directory.
func Dir() string {
	return homedir
}

// Config returns the user config directory.
func Config() string {
	return cmp.Or(
		os.Getenv("XDG_CONFIG_HOME"),
		filepath.Join(Dir(), ".config"),
	)
}

// AppData returns the Windows local app-data directory, falling back to
// the profile's AppData\Local when LOCALAPPDATA is unset. It is only
// meaningful on Windows but returns a sensible path everywhere so
// callers can build paths unconditionally. Single source for the
// config, copilot, and skills/subagents/extensions directory lookups.
func AppData() string {
	return cmp.Or(
		os.Getenv("LOCALAPPDATA"),
		filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local"),
	)
}

// Short replaces the actual home path from [Dir] with `~`.
func Short(p string) string {
	if homedir == "" || !strings.HasPrefix(p, homedir) {
		return p
	}
	if len(p) == len(homedir) {
		return "~"
	}
	if !os.IsPathSeparator(p[len(homedir)]) {
		return p
	}
	return filepath.Join("~", strings.TrimPrefix(p, homedir))
}

// Long replaces the `~` with actual home path from [Dir].
func Long(p string) string {
	if homedir == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	if len(p) == 1 {
		return homedir
	}
	if !os.IsPathSeparator(p[1]) {
		return p
	}
	return strings.Replace(p, "~", homedir, 1)
}
