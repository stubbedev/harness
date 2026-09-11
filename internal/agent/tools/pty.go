package tools

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/term"
)

// The bash tool's synchronous execution runs inside one persistent
// interactive terminal per Crush process: the user's real shell in a
// pseudo-terminal (internal/term), kept alive for the whole session.
// Working directory, environment, virtualenvs and the sudo timestamp
// survive across calls; commands that would hang a pipe-based runner
// (interactive prompts, REPLs, TUIs) just run in the terminal.
//
// Exit codes and the working directory are recovered from a sentinel
// the shell prints after each single-line command. Sudo password
// prompts are detected in the output stream and answered through a
// masked TUI prompt, so the password goes from the user straight to
// the terminal and never enters the model's context; after the first
// authentication the credential stays valid on the session's tty.

const (
	// ptyStartupMs is the quiet window that ends shell startup output
	// (prompt, motd, rc noise) before the first command.
	ptyStartupMs = 800
	// ptySettleMs is how long Input waits for output to go quiet
	// before returning the screen.
	ptySettleMs = 600
	// ptyRestartDelay throttles session restarts after a shell exit.
	ptyRestartDelay = time.Second

	// The sentinel prints the exit code and the working directory after
	// every single-line command. The %d/%s format specifiers keep the
	// echoed command line from matching the parse pattern.
	ptySentinelCmd = `printf '__exit:%d@%s__' "$?" "$PWD"`
)

var (
	// ptySentinelRe captures the exit code and cwd from the sentinel.
	// The greedy (.*) takes everything up to the final "__" on the
	// line, so working directories containing underscores parse.
	ptySentinelRe = regexp.MustCompile(`__exit:(-?\d+)@(.*)__`)
	// ptySentinelLoose matches the sentinel prefix for wait-for; the
	// loose form also matches while the number and path stream in.
	ptySentinelLoose = regexp.MustCompile(`__exit:-?\d+@`)
	// sudoPromptRe matches the password prompt sudo prints when it
	// authenticates against the session's tty.
	sudoPromptRe = regexp.MustCompile(`\[sudo\] password for [^:]+:|[Ss]udo password for [^:]+:`)
	// ptyPromptRe matches the bracketed-paste start marker, which
	// POSIX shells emit right before printing a prompt. Seeing it
	// after a command means the shell has control back.
	ptyPromptRe = regexp.MustCompile(`\x1b\[\?2004h`)
)

// PTYResult is the outcome of a terminal command.
type PTYResult struct {
	// Output is the cleaned terminal output of the command: ANSI
	// stripped, echoed command and sentinel removed, everything after
	// the sentinel (the next prompt) truncated.
	Output string
	// ExitCode is the command's exit code from the sentinel. Nil when
	// the command was still running when the wait budget expired.
	ExitCode *int
	// Cwd is the session's working directory at command completion.
	Cwd string
	// Running reports whether the command had not completed when the
	// wait budget expired.
	Running bool
}

// ptyTerminal is the slice of term.Session the runner uses; it exists
// so tests can substitute a scripted terminal.
type ptyTerminal interface {
	Send(b []byte) error
	WaitForAny(ctx context.Context, patterns []*regexp.Regexp, timeout time.Duration) int
	WaitForQuiet(ctx context.Context, quiet, timeout time.Duration) bool
	Drain() []byte
	Alive() bool
	Close()
}

// ptyRunner owns the single terminal session for one working directory
// and serializes access: commands run one at a time, like a person
// typing in a terminal.
type ptyRunner struct {
	mu      sync.Mutex
	cwd     string
	ask     question.Service
	session ptyTerminal

	startedAt time.Time
	lastEcho  []string
}

var (
	ptyRunnersMu sync.Mutex
	ptyRunners   = map[string]*ptyRunner{}
)

// ptyRunnerFor returns the runner for workingDir, creating (and
// warm-starting) it on first use. The ask service collects sudo
// passwords from the user; nil disables prompting.
func ptyRunnerFor(cwd string, ask question.Service) *ptyRunner {
	ptyRunnersMu.Lock()
	defer ptyRunnersMu.Unlock()
	if r, ok := ptyRunners[cwd]; ok {
		return r
	}
	r := &ptyRunner{cwd: cwd, ask: ask}
	ptyRunners[cwd] = r
	// Warm start in the background: interactive shells (nix,
	// starship) can take hundreds of milliseconds to become ready.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		r.mu.Lock()
		defer r.mu.Unlock()
		if _, err := r.ensureSessionLocked(ctx); err != nil {
			slog.Warn("Terminal session failed to open", "error", err)
		}
	}()
	return r
}

// ensureSessionLocked opens (or reopens after a shell exit) the
// terminal session. Callers must hold r.mu.
func (r *ptyRunner) ensureSessionLocked(ctx context.Context) (ptyTerminal, error) {
	if r.session != nil && r.session.Alive() {
		return r.session, nil
	}
	// The previous shell died (exit, crash): give restarts a small
	// delay so a runaway loop cannot spin.
	if r.session != nil {
		if time.Since(r.startedAt) < ptyRestartDelay {
			time.Sleep(ptyRestartDelay)
		}
	}
	s, err := term.Start(r.cwd)
	if err != nil {
		return nil, err
	}
	r.session = s
	r.startedAt = time.Now()
	// Let the shell settle past its startup output so the first
	// command's output starts clean.
	_ = s.WaitForQuiet(ctx, ptyStartupMs*time.Millisecond, 5*time.Second)
	s.Drain()

	// Strip aliases the user's rc files installed: the session must
	// execute commands with their standard meanings, so `ls` is ls and
	// not `ls -laF --color=auto | less`, and no alias can shadow a
	// command the model intends. Functions and environment from the rc
	// files are kept.
	if err := s.Send([]byte("unalias -a\n")); err == nil {
		_ = s.WaitForQuiet(ctx, ptyStartupMs*time.Millisecond, 5*time.Second)
		s.Drain()
	}
	return s, nil
}

// Run sends a command and waits for its completion sentinel, handling
// sudo password prompts along the way. When the wait budget expires it
// reports the terminal output so far with Running set.
// Run sends a command, waits for the shell to hand control back (the
// bracketed-paste prompt marker), then asks the shell for the
// completion sentinel. The sentinel is only written once the prompt
// returns, so commands that read stdin (read, REPLs, hidden password
// prompts) never swallow it. Sudo password prompts are answered via
// the masked TUI prompt along the way. When the wait budget expires
// the terminal output so far is reported with Running set.
func (r *ptyRunner) Run(ctx context.Context, command string, waitSeconds int) (PTYResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, err := r.ensureSessionLocked(ctx)
	if err != nil {
		return PTYResult{}, err
	}

	if err := s.Send([]byte(command + "\n")); err != nil {
		return PTYResult{}, fmt.Errorf("terminal session: %w", err)
	}
	r.lastEcho = strings.Split(command, "\n")

	deadline := time.Now().Add(time.Duration(waitSeconds) * time.Second)

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		matched := s.WaitForAny(ctx, []*regexp.Regexp{ptyPromptRe, sudoPromptRe}, remaining)
		switch matched {
		case 1: // sudo password prompt
			if err := r.answerSudoPrompt(ctx, s); err != nil {
				return PTYResult{}, err
			}
			// User think time is not the command's budget.
			deadline = deadline.Add(2 * time.Minute)
			continue
		case 0: // prompt returned: command finished
			return r.collectResult(ctx, s)
		}
		break // timeout or session exit
	}

	return PTYResult{Output: r.clean(string(s.Drain())), Running: s.Alive()}, nil
}

// collectResult writes the sentinel now that the shell is at a prompt,
// waits for it, and parses exit code and cwd out of the drained
// output.
func (r *ptyRunner) collectResult(ctx context.Context, s ptyTerminal) (PTYResult, error) {
	if err := s.Send([]byte(ptySentinelCmd + "\n")); err != nil {
		return PTYResult{}, fmt.Errorf("terminal session: %w", err)
	}
	if s.WaitForAny(ctx, []*regexp.Regexp{ptySentinelLoose}, 10*time.Second) != 0 {
		// Sentinel never printed; return what is there without an
		// exit code.
		return PTYResult{Output: r.clean(string(s.Drain()))}, nil
	}

	drained := string(s.Drain())
	cut := drained
	var match []string
	if loc := ptySentinelRe.FindStringIndex(drained); loc != nil {
		match = ptySentinelRe.FindStringSubmatch(drained)
		cut = drained[:loc[0]]
	}
	res := PTYResult{Output: r.clean(cut)}
	if match != nil {
		if code, err := strconv.Atoi(match[1]); err == nil {
			res.ExitCode = &code
		}
		res.Cwd = match[2]
	}
	return res, nil
}

// answerSudoPrompt drains the prompt output, asks the user for the
// password through a masked TUI prompt, and submits it to the
// terminal.
func (r *ptyRunner) answerSudoPrompt(ctx context.Context, s ptyTerminal) error {
	// Drain through the prompt so the answer is not blended into
	// stale output; the prompt itself is re-shown by the TUI.
	_ = s.Drain()

	if r.ask == nil {
		// No question service (e.g. headless run): send ctrl-c so sudo
		// fails fast instead of hanging the whole wait budget.
		_ = s.Send([]byte{0x03})
		return nil
	}

	answers, err := r.ask.Ask(ctx, question.Request{
		Questions: []question.Question{{
			ID:          "sudo_password",
			Type:        question.TypeFreeText,
			Text:        "sudo is asking for your password in the terminal session",
			Description: "The password is written directly to the terminal session and is never shown to the model.",
			Secret:      true,
		}},
	})
	if err != nil {
		// Cancel the sudo prompt so the shell returns to a prompt.
		_ = s.Send([]byte{0x03})
		return nil
	}
	_ = s.Send([]byte(answers[0].FillInText + "\n"))
	return nil
}

// Input sends raw text to the terminal without a sentinel: answers to
// prompts, keystrokes for TUIs, or multiline scripts. Returns the
// terminal after output settles.
func (r *ptyRunner) Input(ctx context.Context, text string) (PTYResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, err := r.ensureSessionLocked(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	if err := s.Send([]byte(text)); err != nil {
		return PTYResult{}, fmt.Errorf("terminal session: %w", err)
	}
	r.lastEcho = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 5*time.Second)
	return PTYResult{Output: r.clean(string(s.Drain())), Running: s.Alive()}, nil
}

// Poll reads the current terminal output without sending anything.
func (r *ptyRunner) Poll(ctx context.Context) (PTYResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, err := r.ensureSessionLocked(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	return PTYResult{Output: r.clean(string(s.Drain())), Running: s.Alive()}, nil
}

// clean normalizes terminal output for the model: CRLF and lone CR to
// LF, ANSI escape sequences stripped, echoed command lines removed,
// blank edges trimmed. Bracketed-paste markers are turned into line
// breaks first: a prompt is printed right after the closing marker, so
// without this, unterminated output (printf %s, ...) glues onto the
// prompt and the next echoed command.
func (r *ptyRunner) clean(raw string) string {
	out := strings.ReplaceAll(raw, "\r\n", "\n")
	out = strings.ReplaceAll(out, "\r", "\n")
	out = strings.ReplaceAll(out, "\x1b[?2004h", "\n")
	out = strings.ReplaceAll(out, "\x1b[?2004l", "\n")
	out = ansi.Strip(out)

	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, ptySentinelCmd) || ptySentinelRe.MatchString(trimmed) || ptySentinelLoose.MatchString(trimmed) {
			continue
		}
		lines = append(lines, line)
	}
	// Drop leading lines that echo what was last sent. The terminal
	// prefixes echoes with its prompt and interleaves paste-marker
	// blank lines, so scan forward and blank each matching line.
	if len(r.lastEcho) > 0 {
		echoIdx := 0
		for i, line := range lines {
			if echoIdx >= len(r.lastEcho) {
				break
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			echo := strings.TrimSpace(r.lastEcho[echoIdx])
			if trimmed == echo || strings.HasSuffix(trimmed, echo) {
				lines[i] = ""
				echoIdx++
			}
		}
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}
