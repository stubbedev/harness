package tools

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/log"
)

var getRg = sync.OnceValue(func() string {
	if testing.Testing() {
		return ""
	}
	path, err := exec.LookPath("rg")
	if err != nil {
		if log.Initialized() {
			slog.Warn("Ripgrep (rg) not found in $PATH. Some grep features might be limited or slower.")
		}
		return ""
	}
	return path
})

func getRgSearchCmd(ctx context.Context, rg, pattern, root, include string) *exec.Cmd {
	// Use -n to show line numbers, -0 for null separation to handle Windows paths
	args := []string{"--json", "-H", "-n", "-0", "--no-require-git", "--no-ignore-dot"}
	for _, name := range fsext.IgnoreFileNames {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			args = append(args, "--ignore-file", name)
		}
	}
	if include != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, "--regexp", pattern, ".")

	cmd := exec.CommandContext(ctx, rg, args...)
	cmd.Dir = root
	return cmd
}
