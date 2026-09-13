package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The session guard (ptySetupCmd, re-asserted by every fence) must keep
// the agent's commands out of the user's shell config in two ways:
// aliases defined by the rc files never expand, and nothing typed at
// the session's prompt ever lands in a history file. These tests drive
// real shells in a real pty, the way the bash tool does, with rc files
// that define an alias and turn on every history mechanism the shell
// has - the exact setup a user machine has.

// bashGuardRunner opens a runner over bash with a HOME that carries a
// .bashrc defining an alias and writing history eagerly, the way a
// user's bash setup does.
func bashGuardRunner(t *testing.T) *ptyRunner {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "bash")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".bashrc"), []byte(
		"alias zzhijack='echo HIJACKED'\n"+
			"HISTFILE=$HOME/.bash_history\n"+
			"PROMPT_COMMAND='history -a'\n"+
			"shopt -s histappend\n",
	), 0o644))
	return newRunnerWithShell(t, "bash")
}

// zshGuardRunner is the zsh equivalent: an alias plus share_history and
// inc_append_history, which write every accepted line to the history
// file immediately.
func zshGuardRunner(t *testing.T) *ptyRunner {
	t.Helper()
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", "zsh")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".zshrc"), []byte(
		"alias zzhijack='echo HIJACKED'\n"+
			"HISTFILE=$HOME/.zsh_history\n"+
			"HISTSIZE=100\n"+
			"SAVEHIST=100\n"+
			"setopt share_history inc_append_history\n",
	), 0o644))
	return newRunnerWithShell(t, "zsh")
}

// historyFree asserts no command the runner executed is in the history
// file, after giving the shell a moment to flush on its way out. The
// only line allowed in the file is the session's own HISTFILE
// plumbing (ptyHistoryOffCmd); anything else is a leak.
func historyFree(t *testing.T, histfile string, commands ...string) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	data, err := os.ReadFile(histfile)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		require.Contains(t, line, "HISTFILE=/dev/null",
			"unexpected line in the history file: %q", line)
	}
	for _, cmd := range commands {
		require.NotContains(t, string(data), cmd,
			"agent command reached the history file")
	}
}

// TestPtyRunner_AliasesNeverExpandBash verifies an alias defined by the
// user's bashrc never applies to a command the agent runs, including
// after the rc files are sourced again mid-session.
func TestPtyRunner_AliasesNeverExpandBash(t *testing.T) {
	r := bashGuardRunner(t)

	res, err := r.Run(t.Context(), "zzhijack", 15)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 127, *res.ExitCode, "the alias must not run; got output %q", res.Output)
	require.NotContains(t, res.Output, "HIJACKED")

	// Re-sourcing the rc files brings the alias definition back; it
	// must still not expand.
	res, err = r.Run(t.Context(), "source ~/.bashrc", 15)
	require.NoError(t, err)
	res, err = r.Run(t.Context(), "zzhijack", 15)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 127, *res.ExitCode, "alias re-added by a re-source must not run; got output %q", res.Output)
	require.NotContains(t, res.Output, "HIJACKED")
}

// TestPtyRunner_AliasesNeverExpandZsh is the zsh twin.
func TestPtyRunner_AliasesNeverExpandZsh(t *testing.T) {
	r := zshGuardRunner(t)

	res, err := r.Run(t.Context(), "zzhijack", 15)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 127, *res.ExitCode, "the alias must not run; got output %q", res.Output)
	require.NotContains(t, res.Output, "HIJACKED")

	res, err = r.Run(t.Context(), "source ~/.zshrc", 15)
	require.NoError(t, err)
	res, err = r.Run(t.Context(), "zzhijack", 15)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 127, *res.ExitCode, "alias re-added by a re-source must not run; got output %q", res.Output)
	require.NotContains(t, res.Output, "HIJACKED")
}

// TestPtyRunner_CommandsNeverEnterHistoryBash verifies no command -
// including the runner's own sentinel and fence lines - lands in the
// history file, even though the bashrc appends eagerly and the shell
// writes its buffer again on exit.
func TestPtyRunner_CommandsNeverEnterHistoryBash(t *testing.T) {
	r := bashGuardRunner(t)

	_, err := r.Run(t.Context(), "echo agent-secret-one", 15)
	require.NoError(t, err)
	_, err = r.Run(t.Context(), "printf %s agent-secret-two", 15)
	require.NoError(t, err)

	home, ok := os.LookupEnv("HOME")
	require.True(t, ok)
	r.Close()
	historyFree(t, filepath.Join(home, ".bash_history"),
		"agent-secret-one", "agent-secret-two", "printf", "__exit_")
}

// TestPtyRunner_CommandsNeverEnterHistoryZsh is the zsh twin: with
// share_history and inc_append_history on, an unguarded session writes
// every line the moment the shell accepts it.
func TestPtyRunner_CommandsNeverEnterHistoryZsh(t *testing.T) {
	r := zshGuardRunner(t)

	_, err := r.Run(t.Context(), "echo agent-secret-one", 15)
	require.NoError(t, err)
	_, err = r.Run(t.Context(), "printf %s agent-secret-two", 15)
	require.NoError(t, err)

	zdot, ok := os.LookupEnv("ZDOTDIR")
	require.True(t, ok)
	r.Close()
	historyFree(t, filepath.Join(zdot, ".zsh_history"),
		"agent-secret-one", "agent-secret-two", "printf", "__exit_")
}

// TestPtyRunner_FenceCarriesGuard pins the fence's shape: the guard
// runs before the marker on the same line, and the marker pattern
// still matches only the printf's output for this sentinel's own tag.
func TestPtyRunner_FenceCarriesGuard(t *testing.T) {
	t.Parallel()

	mark := newSentinel()
	require.True(t, strings.HasPrefix(mark.begin, "unalias -a 2>/dev/null; HISTFILE=/dev/null; printf '"),
		"fence must re-assert the alias and history guard, got %q", mark.begin)
	tag := regexp.MustCompile(`__begin_([0-9a-f]+):`).FindStringSubmatch(mark.begin)
	require.Len(t, tag, 2, "fence marker must carry the sentinel tag")
	require.True(t, mark.beginRe.MatchString("__begin_"+tag[1]+":ok__"))
}
