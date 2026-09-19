package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVerificationHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--verification-helper" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	args := os.Args[index+1:]
	switch args[0] {
	case "pass":
		fmt.Print("verified")
	case "fail":
		fmt.Fprint(os.Stderr, "broken")
		os.Exit(3)
	case "output":
		fmt.Print(strings.Repeat("x", 4096))
		fmt.Fprint(os.Stderr, strings.Repeat("y", 4096))
	case "sleep":
		time.Sleep(30 * time.Second)
	case "modify":
		if err := os.WriteFile("src/main.go", []byte("changed by check"), 0o644); err != nil {
			os.Exit(2)
		}
	case "args":
		fmt.Print(strings.Join(args[1:], "|"))
	}
	os.Exit(0)
}

func helperCommand(t *testing.T, mode string) []string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	return []string{executable, "-test.run=^TestVerificationHelper$", "--", "--verification-helper", mode}
}

func repository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
	}
	git("init", "-q")
	require.NoError(t, os.Mkdir(filepath.Join(root, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("original"), 0o644))
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "initial")
	return root
}

func testConfig(t *testing.T, mode string) VerificationConfig {
	t.Helper()
	return VerificationConfig{Rules: []Rule{{Name: "unit", Paths: []string{"src/**/*.go"}, Inputs: []string{"go.mod"}, Command: helperCommand(t, mode)}}}
}

func TestRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode   string
		status Status
		code   int
		output string
	}{{"pass", Passed, 0, "verified"}, {"fail", Failed, 3, "broken"}, {"modify", Blocked, 0, ""}} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			r, err := New(repository(t), testConfig(t, tc.mode))
			require.NoError(t, err)
			result := r.Run(t.Context(), []string{"src/main.go"})
			require.Equal(t, tc.status, result.Status)
			require.Len(t, result.Checks, 1)
			require.Equal(t, tc.code, result.Checks[0].ExitCode)
			require.Equal(t, tc.output, result.Checks[0].Output)
			require.False(t, result.FinishedAt.Before(result.StartedAt))
			if tc.mode == "modify" {
				require.NotEqual(t, result.RevisionBefore, result.RevisionAfter)
				require.Contains(t, result.Reason, "changed during verification")
			} else {
				require.Equal(t, result.RevisionBefore, result.RevisionAfter)
			}
			var restored struct {
				Verification Result `json:"verification"`
			}
			require.NoError(t, json.Unmarshal([]byte(result.Metadata()), &restored))
			require.Equal(t, result.Status, restored.Verification.Status)
			current, err := r.Current(t.Context(), []string{"src/main.go"}, restored.Verification)
			require.NoError(t, err)
			require.Equal(t, tc.mode != "modify", current)
		})
	}
}

func TestRevisionInvalidation(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"dirty", "untracked", "input", "deleted", "mode", "ignored"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			root := repository(t)
			require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("src/ignored.go\n"), 0o644))
			r, err := New(root, testConfig(t, "pass"))
			require.NoError(t, err)
			result := r.Run(t.Context(), []string{"src/main.go"})
			require.Equal(t, Passed, result.Status)
			require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("unrelated"), 0o644))
			current, err := r.Current(t.Context(), []string{filepath.Join(root, "src", "main.go")}, result)
			require.NoError(t, err)
			require.True(t, current)
			switch change {
			case "dirty":
				require.NoError(t, os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("dirty"), 0o644))
			case "untracked":
				require.NoError(t, os.WriteFile(filepath.Join(root, "src", "new.go"), []byte("new"), 0o644))
			case "input":
				require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module changed"), 0o644))
			case "deleted":
				require.NoError(t, os.Remove(filepath.Join(root, "src", "main.go")))
			case "mode":
				require.NoError(t, os.Chmod(filepath.Join(root, "src", "main.go"), 0o755))
			case "ignored":
				require.NoError(t, os.WriteFile(filepath.Join(root, "src", "ignored.go"), []byte("ignored"), 0o644))
			}
			current, err = r.Current(t.Context(), []string{"src/main.go"}, result)
			require.NoError(t, err)
			require.False(t, current)
		})
	}
}

func TestConfigurationAndLedgerInvalidate(t *testing.T) {
	t.Parallel()
	root := repository(t)
	cfg := testConfig(t, "pass")
	r, err := New(root, cfg)
	require.NoError(t, err)
	result := r.Run(t.Context(), []string{"src/main.go"})
	cfg.Rules[0].Command = helperCommand(t, "fail")
	other, err := New(root, cfg)
	require.NoError(t, err)
	current, err := other.Current(t.Context(), []string{"src/main.go"}, result)
	require.NoError(t, err)
	require.False(t, current)
	current, err = r.Current(t.Context(), []string{"src/main.go", "src/new.go"}, result)
	require.NoError(t, err)
	require.False(t, current)
	require.Equal(t, Passed, r.Run(t.Context(), []string{"src/main.go"}).Status)
}

func TestTimeoutCancellationAndOutput(t *testing.T) {
	t.Parallel()
	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		cfg := testConfig(t, "sleep")
		cfg.Rules[0].TimeoutSeconds = 1
		r, err := New(repository(t), cfg)
		require.NoError(t, err)
		start := time.Now()
		result := r.Run(t.Context(), []string{"src/main.go"})
		require.Equal(t, Blocked, result.Status)
		require.True(t, result.Checks[0].TimedOut)
		require.Less(t, time.Since(start), 5*time.Second)
		require.NotEmpty(t, result.RevisionAfter)
	})
	t.Run("cancel", func(t *testing.T) {
		t.Parallel()
		r, err := New(repository(t), testConfig(t, "sleep"))
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		result := r.Run(ctx, []string{"src/main.go"})
		require.Equal(t, Blocked, result.Status)
		require.Less(t, result.FinishedAt.Sub(result.StartedAt), 5*time.Second)
	})
	t.Run("output", func(t *testing.T) {
		t.Parallel()
		cfg := testConfig(t, "output")
		cfg.Rules[0].MaxOutputBytes = 100
		r, err := New(repository(t), cfg)
		require.NoError(t, err)
		result := r.Run(t.Context(), []string{"src/main.go"})
		require.Equal(t, Passed, result.Status)
		require.Len(t, result.Checks[0].Output, 100)
		require.True(t, result.Checks[0].Truncated)
	})
}

func TestLiteralArguments(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, "args")
	cfg.Rules[0].Command = append(cfg.Rules[0].Command, "$(echo unsafe)", "$HOME", "; touch injected")
	root := repository(t)
	r, err := New(root, cfg)
	require.NoError(t, err)
	result := r.Run(t.Context(), []string{"src/main.go"})
	require.Equal(t, Passed, result.Status)
	require.Equal(t, "$(echo unsafe)|$HOME|; touch injected", result.Checks[0].Output)
	_, err = os.Stat(filepath.Join(root, "injected"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSkippedBlockedAndSelection(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, "pass")
	cfg.Rules = append(cfg.Rules, Rule{Name: "unused", Paths: []string{"docs/**"}, Command: []string{"nonexistent-verification-command"}})
	r, err := New(repository(t), cfg)
	require.NoError(t, err)
	for _, paths := range [][]string{nil, {"README.md"}} {
		result := r.Run(t.Context(), paths)
		require.Equal(t, Skipped, result.Status)
		require.Empty(t, result.Checks)
	}
	require.Equal(t, Passed, r.Run(t.Context(), []string{"src/main.go"}).Status)
	require.Equal(t, Blocked, r.Run(t.Context(), []string{"docs/new.md"}).Status)
	require.Equal(t, Blocked, r.Run(t.Context(), []string{"../outside"}).Status)
	r, err = New(t.TempDir(), VerificationConfig{})
	require.NoError(t, err)
	require.Equal(t, Skipped, r.Run(t.Context(), []string{"new.go"}).Status)
}

func TestCompletionGate(t *testing.T) {
	t.Parallel()
	root := repository(t)
	cfg := testConfig(t, "pass")
	cfg.RequireOnCompletion = true
	cfg.MaxRepairAttempts = 1
	r, err := New(root, cfg)
	require.NoError(t, err)
	paths := []string{"src/main.go"}
	require.Equal(t, "verify", r.Gate(t.Context(), paths, nil, 0).Action)
	require.True(t, r.Gate(t.Context(), nil, nil, 0).Allow)
	result := r.Run(t.Context(), paths)
	require.True(t, r.Gate(t.Context(), paths, &result, 0).Allow)
	result.Status = Failed
	require.Equal(t, "repair", r.Gate(t.Context(), paths, &result, 0).Action)
	require.Equal(t, "blocked", r.Gate(t.Context(), paths, &result, 1).Action)
	result.Status = Blocked
	require.Equal(t, "blocked", r.Gate(t.Context(), paths, &result, 0).Action)
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("changed"), 0o644))
	require.Equal(t, "verify", r.Gate(t.Context(), paths, &result, 0).Action)
	cfg.RequireOnCompletion = false
	r, err = New(root, cfg)
	require.NoError(t, err)
	require.True(t, r.Gate(t.Context(), paths, nil, 0).Allow)
}

func TestValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*VerificationConfig)
	}{
		{"empty name", func(c *VerificationConfig) { c.Rules[0].Name = "" }},
		{"duplicate", func(c *VerificationConfig) { c.Rules = append(c.Rules, c.Rules[0]) }},
		{"no paths", func(c *VerificationConfig) { c.Rules[0].Paths = nil }},
		{"bad glob", func(c *VerificationConfig) { c.Rules[0].Paths = []string{"["} }},
		{"outside", func(c *VerificationConfig) { c.Rules[0].Paths = []string{"../**"} }},
		{"absolute", func(c *VerificationConfig) { c.Rules[0].Inputs = []string{"/tmp/**"} }},
		{"no argv", func(c *VerificationConfig) { c.Rules[0].Command = nil }},
		{"nul argv", func(c *VerificationConfig) { c.Rules[0].Command = []string{"echo", "\x00"} }},
		{"timeout", func(c *VerificationConfig) { c.Rules[0].TimeoutSeconds = -1 }},
		{"output", func(c *VerificationConfig) { c.Rules[0].MaxOutputBytes = 1024*1024 + 1 }},
		{"repairs", func(c *VerificationConfig) { c.MaxRepairAttempts = 11 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig(t, "pass")
			tc.mutate(&cfg)
			require.Error(t, cfg.Validate())
		})
	}
	require.NoError(t, VerificationConfig{}.Validate())
	require.Equal(t, DefaultTimeout, Rule{}.timeout())
	require.Equal(t, DefaultMaxOutputBytes, Rule{}.outputLimit())
}
