package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed gitignore/old
var oldGitIgnore string

//go:embed gitignore/default
var defaultGitIgnore string

// EnsureDataDir creates a workspace data directory and the .gitignore
// that keeps it out of version control, upgrading a .gitignore written
// by an older release. Local and server-created workspaces both go
// through it, so they always end up with the same file.
func EnsureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create data directory: %q %w", dir, err)
	}

	gitIgnorePath := filepath.Join(dir, ".gitignore")
	content, err := os.ReadFile(gitIgnorePath)
	if os.IsNotExist(err) || string(content) == oldGitIgnore {
		if err := os.WriteFile(gitIgnorePath, []byte(defaultGitIgnore), 0o644); err != nil {
			return fmt.Errorf("failed to create .gitignore file: %q %w", gitIgnorePath, err)
		}
	}
	return nil
}
