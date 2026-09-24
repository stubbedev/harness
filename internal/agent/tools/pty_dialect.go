package tools

import "github.com/stubbedev/harness/internal/term"

// shellDialect is the session protocol for one family of shells: how to
// ask it for an exit code and a working directory, how to fence a
// command so a call only reports its own output, and how to quiet its
// history and install the prompt marker.
//
// There is a dialect rather than a POSIX-only path with an embedded
// interpreter standing in elsewhere, because the point of the tool is to
// hand the model the real shell the user launched from. The model is
// told which shell that is and already knows how to write for it; it is
// only this protocol plumbing that has to be translated.
//
// sentinelCmd, fenceCmd and the two patterns are format strings taking
// the session's random tag, so a literal percent must be doubled.
type shellDialect struct {
	sentinelCmd   string
	sentinelRe    string
	sentinelLoose string
	fenceCmd      string
	fenceRe       string
	historyOffCmd string
	setupCmd      string
}

// The exit marker and fence patterns are the same text in every dialect;
// only the command that prints them differs.
const (
	dialectSentinelRe    = `__exit_%s:(-?\d+)@(.*)__`
	dialectSentinelLoose = `__exit_%s:-?\d+@`
	dialectFenceRe       = `__begin_%s:ok__`
)

// posixDialect drives bash, zsh and the rest of the Bourne family.
//
// The sentinel's %%d/%%s keep the echoed command line from matching the
// parse pattern. The sentinel and the fence both re-assert the session
// guard: a command that re-sources the user's rc files (source
// ~/.zshrc, direnv, a nested shell) can bring aliases and history
// settings back, and the next command must not inherit them. The
// sentinel runs after every command and the fence only before a command
// that finds the session in an unknown state (see runCommand), so the
// sentinel is what carries the guard from one command to the next; its
// printf goes first, while $? is still the command's.
//
// Each switch in setupCmd is stderr-guarded so one line works across
// zsh (setopt/unalias -a), bash (shopt) and plain POSIX shells, where
// the unknown builtins just error on stderr while the rest of the line
// runs. One switch earns its absence: zsh treats an unknown option to
// its own `set` builtin as a parse error and refuses the WHOLE line, so
// no `set +o history` may appear here -- it would silently keep the
// prompt marker from ever installing.
var posixDialect = shellDialect{
	sentinelCmd:   `printf '__exit_%s:%%d@%%s__' "$?" "$PWD"; unalias -a 2>/dev/null; HISTFILE=/dev/null`,
	sentinelRe:    dialectSentinelRe,
	sentinelLoose: dialectSentinelLoose,
	fenceCmd:      `unalias -a 2>/dev/null; HISTFILE=/dev/null; printf '__begin_%s:%%s__' "ok"`,
	fenceRe:       dialectFenceRe,
	historyOffCmd: `HISTFILE=/dev/null`,
	setupCmd: `unalias -a 2>/dev/null; setopt no_aliases 2>/dev/null; shopt -u expand_aliases 2>/dev/null; ` +
		`HISTFILE=/dev/null; SAVEHIST=0; shopt -u histappend 2>/dev/null; ` +
		`unsetopt share_history inc_append_history inc_append_history_time append_history 2>/dev/null; ` +
		`PROMPT_COMMAND=""; RPS1=""; RPROMPT=""; PS2=""; PS1="$(printf '\033]133;A\007')"`,
}

// powershellDialect drives Windows PowerShell and PowerShell Core.
//
// Exit codes need both of PowerShell's answers: $LASTEXITCODE is the
// code of the last native program and is null until one has run, while
// $? is a boolean covering cmdlets too. $? has to be captured first,
// because the assignment that reads $LASTEXITCODE sets it to true.
// The sentinel and the fence clear $LASTEXITCODE so a code left over
// from an earlier native command is never reported as this one's.
//
// Aliases are deliberately left alone, unlike POSIX. In PowerShell ls,
// cat and rm are shipped aliases for real cmdlets, so stripping them
// would make the shell less like the one the model expects, not more.
var powershellDialect = shellDialect{
	sentinelCmd: `$__ok = $?; $__hc = $LASTEXITCODE; ` +
		`if ($null -eq $__hc) { $__hc = $(if ($__ok) { 0 } else { 1 }) }; ` +
		`[Console]::Write("__exit_%s:$__hc@$($PWD.Path)__"); $global:LASTEXITCODE = $null`,
	sentinelRe:    dialectSentinelRe,
	sentinelLoose: dialectSentinelLoose,
	fenceCmd:      `$global:LASTEXITCODE = $null; [Console]::Write("__begin_%s:ok__")`,
	fenceRe:       dialectFenceRe,
	historyOffCmd: `if (Get-Command Set-PSReadLineOption -ErrorAction SilentlyContinue) { Set-PSReadLineOption -HistorySaveStyle SaveNothing }`,
	setupCmd:      `function global:prompt { "$([char]27)]133;A$([char]7)" }`,
}

// cmdDialect drives cmd.exe. It is the thinnest of the three: cmd keeps
// no history file to sandbox and has no aliases to strip, and its PROMPT
// builtin spells an escape as $E, so the marker ends with ST rather than
// the BEL the other dialects use -- ptyPromptSplitRe accepts both.
var cmdDialect = shellDialect{
	sentinelCmd:   `echo __exit_%s:%%ERRORLEVEL%%@%%CD%%__`,
	sentinelRe:    dialectSentinelRe,
	sentinelLoose: dialectSentinelLoose,
	fenceCmd:      `echo __begin_%s:ok__`,
	fenceRe:       dialectFenceRe,
	historyOffCmd: "",
	setupCmd:      `prompt $E]133;A$E\`,
}

// dialectFor returns the protocol for the shell at path, defaulting to
// POSIX for a shell this package does not recognise: its sentinel is
// plain `printf` and works in more shells than any other guess would.
func dialectFor(path string) shellDialect {
	switch term.KindOf(path) {
	case term.KindPowerShell:
		return powershellDialect
	case term.KindCmd:
		return cmdDialect
	default:
		return posixDialect
	}
}
