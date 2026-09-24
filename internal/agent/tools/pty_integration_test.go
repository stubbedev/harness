package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// shellOrSkip returns the path of the named shell, skipping when this
// machine does not have it.
func shellOrSkip(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("no %s on this machine", name)
	}
	return path
}

// runOne runs one command in a fresh runner over shell and returns its
// output, requiring it to succeed.
func runOne(t *testing.T, shell, command string) string {
	t.Helper()
	r := newRunnerWithShell(t, shell)
	require.Equal(t, ptyPromptRe, r.promptRe, "the startup hook installs the prompt marker")
	res, err := r.Type(t.Context(), command, 10)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	return res.Output
}

func TestShellIntegration_ZshSourcesUserFilesThroughMovedZdotdir(t *testing.T) {
	zsh := shellOrSkip(t, "zsh")
	home := t.TempDir()
	conf := filepath.Join(home, ".config", "zsh")
	require.NoError(t, os.MkdirAll(conf, 0o755))
	// The common layout: ~/.zshenv moves ZDOTDIR, and the rc file lives
	// there.
	require.NoError(t, os.WriteFile(filepath.Join(home, ".zshenv"), []byte("ZDOTDIR=$HOME/.config/zsh\nexport FROM_ENV=1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(conf, ".zshrc"), []byte("export FROM_RC=1\nalias ls='ls --boom'\nHISTFILE=$HOME/.zsh_history\n"), 0o644))
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")

	out := runOne(t, zsh, `echo "$FROM_ENV $FROM_RC $HISTFILE $ZDOTDIR"; alias ls 2>/dev/null; [[ -o zle ]] && echo zle-on; true`)
	require.Equal(t, "1 1 /dev/null "+conf, out)
}

func TestShellIntegration_BashSourcesBashrc(t *testing.T) {
	bash := shellOrSkip(t, "bash")
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".bashrc"), []byte("export FROM_RC=1\nalias ls='ls --boom'\nHISTFILE=$HOME/.bash_history\n"), 0o644))
	t.Setenv("HOME", home)

	out := runOne(t, bash, `echo "$FROM_RC $HISTFILE"; alias ls 2>/dev/null; [[ -o emacs || -o vi ]] && echo editor-on; true`)
	require.Equal(t, "1 /dev/null", out)
}

func TestShellIntegration_ShSourcesUserEnv(t *testing.T) {
	sh := shellOrSkip(t, "sh")
	env := filepath.Join(t.TempDir(), "env.sh")
	require.NoError(t, os.WriteFile(env, []byte("FROM_ENV=1\n"), 0o644))
	t.Setenv("ENV", env)

	out := runOne(t, sh, `echo "$FROM_ENV $HISTFILE ${HARNESS_USER_ENV-unset} $ENV"`)
	require.Equal(t, "1 /dev/null unset "+env, out)
}

func TestShellIntegration_UnknownShellHasNoHook(t *testing.T) {
	t.Parallel()
	_, ok := launchFor("/usr/bin/fish", posixDialect)
	require.False(t, ok)
	_, ok = launchFor("pwsh.exe", powershellDialect)
	require.False(t, ok)
}

func TestIntegrationDir_IsContentAddressed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	a, err := integrationDir(map[string]string{"rc": "one"})
	require.NoError(t, err)
	b, err := integrationDir(map[string]string{"rc": "two"})
	require.NoError(t, err)
	require.NotEqual(t, a, b)
	got, err := os.ReadFile(filepath.Join(a, "rc"))
	require.NoError(t, err)
	require.Equal(t, "one", string(got))
}

func TestShellSession_CarriesAgentMarkers(t *testing.T) {
	sh := shellOrSkip(t, "sh")
	require.Equal(t, "1 harness harness", runOne(t, sh, `echo "$HARNESS $AGENT $AI_AGENT"`))
}
