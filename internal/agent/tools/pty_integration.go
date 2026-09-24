package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// A terminal session's setup - history sandboxed, aliases stripped, the
// prompt marker installed - is handed to the shell through its own
// startup hooks where the shell has them, the way terminal emulators
// install their shell integration: zsh reads its rc files from ZDOTDIR,
// bash takes --rcfile, and a POSIX sh sources $ENV. Each hook points at
// a small rc file of Harness's own that sources the user's real one and
// then runs the setup, so the session is the user's shell exactly as
// they configured it, with the setup applied last.
//
// Nothing is typed, so nothing can be recorded to the user's history or
// echoed back as debris, and the first prompt the shell prints already
// carries the marker: startup ends the moment the shell is interactive.
// Setting HISTFILE before the rc files finish also keeps zsh and bash
// from loading the user's history file at all. Shells with no such hook
// (fish, the Windows shells) are set up by typing, see
// setupSessionLocked.

// shellLaunch is how one shell is started so that it sets itself up.
type shellLaunch struct {
	args []string
	env  []string
}

// zshIntegration are the rc files of a ZDOTDIR that runs the user's own
// startup files and then the session setup. zsh reads .zshenv,
// .zprofile (login shells) and .zshrc from $ZDOTDIR; each one here swaps
// the user's directory back in around sourcing its counterpart, so the
// user's files see the ZDOTDIR they expect, and a .zshenv that moves
// ZDOTDIR (the common ~/.config/zsh setup) is followed. .zshrc leaves
// the user's directory in place, so .zlogin, nested shells and anything
// reading ZDOTDIR later see theirs, not this one.
func zshIntegration(setup string) map[string]string {
	swap := func(name string) string {
		return `_harness_zdotdir=$ZDOTDIR
ZDOTDIR=$HARNESS_USER_ZDOTDIR
[[ -r "$ZDOTDIR/` + name + `" ]] && builtin source "$ZDOTDIR/` + name + `"
HARNESS_USER_ZDOTDIR=${ZDOTDIR:-$HOME}
ZDOTDIR=$_harness_zdotdir
builtin unset _harness_zdotdir
`
	}
	return map[string]string{
		".zshenv":   swap(".zshenv"),
		".zprofile": swap(".zprofile"),
		".zshrc": `ZDOTDIR=$HARNESS_USER_ZDOTDIR
builtin unset HARNESS_USER_ZDOTDIR
[[ -r "$ZDOTDIR/.zshrc" ]] && builtin source "$ZDOTDIR/.zshrc"
` + setup + "\n" + zshNoEditor + "\n",
	}
}

// zshNoEditor and bashNoEditor turn the shell's line editor off for the
// session: the shell reads each line as the terminal delivers it, like
// dash does. The editor is there for a person at the keyboard -
// highlighting redrawn on every keystroke, autosuggestions, completion -
// and for the model it is only cost: a redraw per line (the bulk of what
// a command's round trip spent in zsh), echo debris the cleaner has to
// pick apart, and a multiline command having to go in as a bracketed
// paste and wait for the editor's echo of it to settle. Programs the
// session runs keep their own editors; only the shell's prompt loses
// completion and history recall, which nothing drives it by.
const (
	zshNoEditor  = `unsetopt zle`
	bashNoEditor = `set +o emacs +o vi`
)

// bashIntegration is the --rcfile of a bash session: the user's
// ~/.bashrc, as an interactive bash would have read it (the system
// bashrc is read either way), then the setup.
func bashIntegration(setup string) map[string]string {
	return map[string]string{
		"bashrc": `[ -r ~/.bashrc ] && . ~/.bashrc
` + setup + "\n" + bashNoEditor + "\n",
	}
}

// shIntegration is the $ENV file of a POSIX sh session: the user's own
// $ENV, if they had one, then the setup. ENV is put back to the user's
// first: it is exported, and a nested interactive sh reading this file
// too would install the prompt marker in a shell the session does not
// drive, whose prompt would read as the command finishing.
func shIntegration(setup string) map[string]string {
	return map[string]string{
		"env.sh": `if [ -n "$HARNESS_USER_ENV" ]; then ENV=$HARNESS_USER_ENV; else unset ENV; fi
unset HARNESS_USER_ENV
[ -n "$ENV" ] && [ -r "$ENV" ] && . "$ENV"
` + setup + "\n",
	}
}

var (
	integrationMu   sync.Mutex
	integrationDirs = map[string]string{}
)

// integrationDir writes files into a directory named for their content
// and returns it. The name makes the directory safe to share between
// Harness processes and versions: a directory that exists already holds
// exactly these files. Each process writes it at most once.
func integrationDir(files map[string]string) (string, error) {
	h := sha256.New()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	// Map order is random; the name must not be.
	slices.Sort(names)
	for _, name := range names {
		fmt.Fprintf(h, "%s\x00%s\x00", name, files[name])
	}
	sum := hex.EncodeToString(h.Sum(nil))[:16]

	integrationMu.Lock()
	defer integrationMu.Unlock()
	// A directory written earlier is reused while its files are still
	// there: a cache cleaned under a running process (or a test's
	// temporary home gone) is written again rather than handed to a
	// shell that would start without its setup.
	if dir, ok := integrationDirs[sum]; ok && filesPresent(dir, names) {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "harness", "shell-integration", sum)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		if b, err := os.ReadFile(path); err == nil && string(b) == files[name] {
			continue
		}
		// Written aside and renamed in, so a shell starting in another
		// process never reads half a file.
		tmp, err := os.CreateTemp(dir, name+".*")
		if err != nil {
			return "", err
		}
		_, werr := tmp.WriteString(files[name])
		cerr := tmp.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(tmp.Name())
			return "", fmt.Errorf("write shell integration: %w", errors.Join(werr, cerr))
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			_ = os.Remove(tmp.Name())
			return "", err
		}
	}
	integrationDirs[sum] = dir
	return dir, nil
}

// filesPresent reports whether every one of names exists in dir.
func filesPresent(dir string, names []string) bool {
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

// launchFor returns how to start the shell at shellPath so that it runs
// the dialect's setup itself, or false when the shell has no startup
// hook to hand it (the setup is then typed).
func launchFor(shellPath string, dialect shellDialect) (shellLaunch, bool) {
	if dialect.setupCmd != posixDialect.setupCmd {
		return shellLaunch{}, false
	}
	setup := dialect.setupCmd
	switch name := strings.TrimSuffix(strings.ToLower(filepath.Base(shellPath)), ".exe"); name {
	case "zsh":
		dir, err := integrationDir(zshIntegration(setup))
		if err != nil {
			return shellLaunch{}, false
		}
		user := os.Getenv("ZDOTDIR")
		if user == "" {
			user, _ = os.UserHomeDir()
		}
		return shellLaunch{env: []string{"ZDOTDIR=" + dir, "HARNESS_USER_ZDOTDIR=" + user}}, true
	case "bash":
		dir, err := integrationDir(bashIntegration(setup))
		if err != nil {
			return shellLaunch{}, false
		}
		return shellLaunch{args: []string{"--rcfile", filepath.Join(dir, "bashrc")}}, true
	case "sh", "dash", "ash", "ksh", "mksh":
		dir, err := integrationDir(shIntegration(setup))
		if err != nil {
			return shellLaunch{}, false
		}
		return shellLaunch{env: []string{
			"ENV=" + filepath.Join(dir, "env.sh"),
			"HARNESS_USER_ENV=" + os.Getenv("ENV"),
		}}, true
	default:
		return shellLaunch{}, false
	}
}
