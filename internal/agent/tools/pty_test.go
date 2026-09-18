package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/term"
)

// newTestRunner opens a runner over a real /bin/sh session in a temp
// directory: fast, deterministic, no rc files.
func newTestRunner(t *testing.T) *ptyRunner {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	return newRunnerWithShell(t, "/bin/sh")
}

// newBracketedPasteRunner opens a runner over a shell that asks for bracketed
// paste. Whether a shell asks is the shell's business, not ours -- /bin/sh is
// dash on Debian and bash 3.2 on macOS, and a bash built without readline asks
// for nothing either -- so try the shells this machine has and skip when none
// of them turns the mode on.
func newBracketedPasteRunner(t *testing.T) *ptyRunner {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	candidates := []string{"/bin/bash"}
	if p, err := exec.LookPath("bash"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, "/bin/sh")

	for _, shell := range candidates {
		if _, err := os.Stat(shell); err != nil {
			continue
		}
		r := newRunnerWithShell(t, shell)
		if r.session.BracketedPaste() {
			return r
		}
		// Nil out the session so the runner's cleanup does not close it twice.
		r.session.Close()
		r.session = nil
	}
	t.Skip("no shell on this machine asks for bracketed paste")
	return nil
}

// newRunnerWithShell opens a runner over the given shell in a temp directory.
func newRunnerWithShell(t *testing.T, shell string) *ptyRunner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pty sessions are unsupported on windows")
	}
	t.Setenv("SHELL", shell)
	r := &ptyRunner{cwd: t.TempDir()}
	t.Cleanup(func() {
		if r.session != nil {
			r.session.Close()
		}
	})
	// Open synchronously so tests observe the session directly. Go through
	// terminal() rather than ensureSessionLocked: the latter needs r.mu,
	// which the warm-start goroutine in ptyRunnerFor also takes.
	_, err := r.terminal(t.Context())
	require.NoError(t, err)
	return r
}

func TestPtyRunner_RunEcho(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "echo hello", 10)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "hello", res.Output)
	require.False(t, res.Running)
	require.NotEmpty(t, res.Cwd)
}

func TestPtyRunner_RunNonzeroExit(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "false", 10)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 1, *res.ExitCode)
}

func TestPtyRunner_StatePersistsAcrossCalls(t *testing.T) {
	r := newTestRunner(t)

	_, err := r.Type(t.Context(), "export PTY_TEST_VAR=persisted", 10)
	require.NoError(t, err)

	res, err := r.Type(t.Context(), "printf %s \"$PTY_TEST_VAR\"", 10)
	require.NoError(t, err)
	require.Equal(t, "persisted", res.Output)
}

func TestPtyRunner_CwdTracks(t *testing.T) {
	r := newTestRunner(t)

	sub := filepath.Join(r.cwd, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	res, err := r.Type(t.Context(), "cd sub", 10)
	require.NoError(t, err)
	// The shell reports the directory it is actually in, which is the
	// resolved one: on macOS the temp directory lives under /var, a
	// symlink to /private/var, so comparing the two spellings literally
	// fails there while testing nothing about tracking the cd.
	require.Equal(t, resolved(t, sub), resolved(t, res.Cwd))
}

// resolved is a path with every symlink along it followed, so two
// spellings of the same directory compare equal.
func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return real
}

func TestPtyRunner_StillRunning(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "sleep 5", 1)
	require.NoError(t, err)
	require.True(t, res.Running)
	require.Nil(t, res.ExitCode)

	// Interrupt the sleeper so the session is clean for teardown.
	_, err = r.Type(t.Context(), "\x03", 10)
	require.NoError(t, err)
}

// A command that stops to ask something is detected from the process
// state, not waited out to the budget: the call returns as waiting for
// input within a couple of seconds even with a minute of budget left.
//
// That detection reads the foreground job's state out of procfs, so it
// exists on Linux and nowhere else (see term.SampleJob). Everywhere
// else the call has nothing to detect with and falls back to its wait
// budget, reporting the command as still running - which is the
// behaviour asserted below, with a budget short enough to be worth
// waiting for.
func TestPtyRunner_RunReturnsWhenInputNeeded(t *testing.T) {
	r := newTestRunner(t)

	if runtime.GOOS != "linux" {
		res, err := r.Type(t.Context(), "read answer; echo \"got:$answer\"", 2)
		require.NoError(t, err)
		require.True(t, res.Running)
		require.Nil(t, res.ExitCode)

		done, err := r.Type(t.Context(), "hello\n", 10)
		require.NoError(t, err)
		require.Contains(t, done.Output, "got:hello")
		return
	}

	start := time.Now()
	res, err := r.Type(t.Context(), "read answer; echo \"got:$answer\"", 60)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 20*time.Second, "the call should return on the quiet window, not the budget")
	require.True(t, res.Running)
	require.True(t, res.Waiting)
	require.Nil(t, res.ExitCode)

	done, err := r.Type(t.Context(), "hello\n", 10)
	require.NoError(t, err)
	require.Contains(t, done.Output, "got:hello")
}

// A command left running by a timed-out call becomes an orphan: polling
// waits for it to finish and reports its exit code and output, rather
// than returning an instant snapshot the caller has to keep re-taking.
func TestPtyRunner_PollWaitsForOrphanedCommand(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "sleep 3; echo late", 1)
	require.NoError(t, err)
	require.True(t, res.Running)

	poll, err := r.Poll(t.Context())
	require.NoError(t, err)
	require.NotNil(t, poll.ExitCode, "the poll waited for the orphan to finish")
	require.Equal(t, 0, *poll.ExitCode)
	require.Contains(t, poll.Output, "late")
	require.False(t, poll.Running)
}

// A command that keeps producing output past its wait budget is making
// progress: the wait is leased forward and the call returns its real
// completion, not a still-running snapshot the agent would poll for.
func TestPtyRunner_StreamingCommandLeasesPastBudget(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "for i in 1 2 3 4 5 6; do echo tick $i; sleep 0.4; done", 2)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode, "the lease should have carried the call to completion")
	require.Equal(t, 0, *res.ExitCode)
	require.False(t, res.Running)
	require.Contains(t, res.Output, "tick 6")
}

func TestPtyRunner_InputAnswersPrompt(t *testing.T) {
	r := newTestRunner(t)

	// A program reading stdin hangs a pipe-based runner; in the
	// terminal it just waits until Input feeds it.
	_, err := r.Type(t.Context(), "read answer; echo \"got:$answer\"", 1)
	require.NoError(t, err) // returns as still running

	res, err := r.Type(t.Context(), "hello\n", 10)
	require.NoError(t, err)
	require.Contains(t, res.Output, "got:hello")

	// Verify the session is back at a prompt.
	done, err := r.Type(t.Context(), "true", 10)
	require.NoError(t, err)
	require.NotNil(t, done.ExitCode)
}

// fakeAsk scripts the masked credential prompts: each Ask pops the next
// scripted answer (ErrCancelled once they run out) and records the
// request for assertions.
type fakeAsk struct {
	mu       sync.Mutex
	answers  []string
	requests []question.Request
}

func (f *fakeAsk) Ask(_ context.Context, req question.Request) ([]question.Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if len(f.answers) == 0 {
		return nil, question.ErrCancelled
	}
	answer := f.answers[0]
	f.answers = f.answers[1:]
	return []question.Answer{{QuestionID: req.Questions[0].ID, FillInText: answer}}, nil
}

func (f *fakeAsk) asks() []question.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]question.Request{}, f.requests...)
}

func (f *fakeAsk) Subscribe(context.Context) <-chan pubsub.Event[question.Request] { return nil }

func (f *fakeAsk) SubscribeNotifications(context.Context) <-chan pubsub.Event[question.Notification] {
	return nil
}

func (f *fakeAsk) Answer([]question.Answer) bool { return false }

func (f *fakeAsk) Cancel() bool { return false }

// newAskRunner opens a runner whose credential prompts are answered from
// a script.
func newAskRunner(t *testing.T, answers ...string) (*ptyRunner, *fakeAsk) {
	t.Helper()
	r := newTestRunner(t)
	ask := &fakeAsk{answers: answers}
	r.setState(func() { r.ask = ask })
	return r, ask
}

// A localized sudo prompt - the shape the old English-only regex missed
// - opens the masked dialog: the text is prompt-shaped and the reader
// has switched the terminal to a hidden line. The answer goes to the
// terminal and neither the prompt nor the password reaches the output.
func TestPtyRunner_LocalizedSudoPromptOpensMaskedDialog(t *testing.T) {
	r, ask := newAskRunner(t, "hunter2")

	res, err := r.Type(t.Context(),
		`/bin/sh -c 'stty -echo; printf "[sudo] Passwort für stubbe: "; read pw; stty echo; printf ok'`, 30)
	require.NoError(t, err)
	require.Len(t, ask.asks(), 1)
	require.True(t, ask.asks()[0].Questions[0].Secret)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "ok", res.Output)
	require.NotContains(t, res.Output, "hunter2")
}

// sudo since 1.9 reads the password itself, keystroke by keystroke: echo
// off and canonical mode off together, which is also how every line
// editor reads. The prompt text is what settles it, so this opens the
// masked dialog like the canonical readers do.
func TestPtyRunner_RawModeSudoPromptOpensMaskedDialog(t *testing.T) {
	r, ask := newAskRunner(t, "hunter2")

	res, err := r.Type(t.Context(),
		`/bin/sh -c 'stty -echo -icanon; printf "[sudo] password for stubbe: "; read pw; stty echo icanon; printf ok'`, 30)
	require.NoError(t, err)
	require.Len(t, ask.asks(), 1)
	require.True(t, ask.asks()[0].Questions[0].Secret)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "ok", res.Output)
	require.NotContains(t, res.Output, "hunter2")
}

// A command that merely prints the word and exits must not open the
// dialog, even under a shell whose line editor leaves the idle terminal
// in the same raw, no-echo state sudo reads in: the shell's prompt
// marker says the command is over and nothing is asking.
func TestPtyRunner_PrintedPasswordUnderLineEditorIsNotMasked(t *testing.T) {
	r := newBracketedPasteRunner(t)
	ask := &fakeAsk{}
	r.setState(func() { r.ask = ask })

	res, err := r.Type(t.Context(), `printf 'db password: hunter2\n'`, 30)
	require.NoError(t, err)
	require.Empty(t, ask.asks())
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "db password: hunter2", res.Output)
}

// A setuid job hides its wait point from this user, so once it goes
// quiet nothing says whether it is reading the terminal or sitting in
// PAM's fail delay after a rejected password. The runner holds the
// "waiting for input" verdict past that delay rather than handing the
// model a question that is about to answer itself.
func TestPtyRunner_BlindQuietJobIsNotWaitingRightAway(t *testing.T) {
	r, _ := newAskRunner(t)
	_, err := r.Type(t.Context(), "true", 10)
	require.NoError(t, err)

	blind := &blindSleeperTerm{since: time.Now()}
	start := time.Now()
	res, err := r.awaitCompletion(t.Context(), blind, nil, 2)
	require.NoError(t, err)
	require.True(t, res.Running)
	require.False(t, res.Waiting, "a silent job with no visible wait point is not a question yet")
	require.GreaterOrEqual(t, time.Since(start), 1500*time.Millisecond, "the wait ran the budget out rather than deciding early")
}

// blindSleeperTerm is a terminal whose foreground job is asleep with
// echo on and an unreadable wait point - a setuid program between two
// password prompts - and never prints anything.
type blindSleeperTerm struct {
	stuckReaderTerm
	since time.Time
}

func (b *blindSleeperTerm) IdleFor() time.Duration { return time.Since(b.since) }

func (b *blindSleeperTerm) WaitForOutput(ctx context.Context, timeout time.Duration) bool {
	select {
	case <-ctx.Done():
	case <-time.After(timeout):
	}
	return false
}

func (b *blindSleeperTerm) SampleJob() term.JobActivity {
	return term.JobActivity{Observed: true, Asleep: true}
}

func (b *blindSleeperTerm) SecretRead() term.SecretReadState { return term.SecretReadNo }

// A generic Password: prompt - su, docker login and friends - opens the
// masked dialog even when nothing in the output says "password" in a
// language the text hint knows: the foreground job is blocked in a
// hidden-line read, which is the credential-reader state regardless of
// what its prompt says.
func TestPtyRunner_HiddenLineReadWithoutKnownPromptOpensMaskedDialog(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the quiet-window job inspection behind this test is Linux-only (see term.SampleJob)")
	}
	r, ask := newAskRunner(t, "hunter2")

	res, err := r.Type(t.Context(),
		`/bin/sh -c 'stty -echo; printf "Passord: "; read pw; stty echo; printf ok'`, 30)
	require.NoError(t, err)
	require.Len(t, ask.asks(), 1)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "ok", res.Output)
}

// A program that merely prints the word "password" while asking an
// ordinary question is not intercepted: its echo stays on, so the model
// gets the waiting-for-input result and answers with input.
func TestPtyRunner_PlainQuestionIsNotMasked(t *testing.T) {
	r, ask := newAskRunner(t)

	res, err := r.Type(t.Context(),
		`/bin/sh -c 'printf "Choose a password policy name: "; read name; printf "got:%s" "$name"'`, 30)
	require.NoError(t, err)
	require.Empty(t, ask.asks())
	require.Nil(t, res.ExitCode)
	if runtime.GOOS == "linux" {
		require.True(t, res.Waiting, "an ordinary question waits for input from the model")
	}

	done, err := r.Type(t.Context(), "alpha\n", 10)
	require.NoError(t, err)
	require.Contains(t, done.Output, "got:alpha")
}

// A wrong password re-opens the masked dialog - the reader clears echo
// again for the retry - and the user is told the previous attempt was
// rejected. Neither attempt leaks into the output: echo was off.
func TestPtyRunner_WrongPasswordReopensMaskedDialog(t *testing.T) {
	r, ask := newAskRunner(t, "wrong-one", "open-sesame")

	// The command line spells out both prompts and the rejection, so
	// the tty echo of it carries text the prompt hint matches before
	// anything has asked for anything: the two dialogs here are the two
	// real reads, not the echo of the command that does them.
	res, err := r.Type(t.Context(),
		`/bin/sh -c 'stty -echo; printf "Password: "; read a; `+
			`printf "\nSorry, try again.\n"; printf "Password: "; read b; stty echo; `+
			`[ "$b" = open-sesame ] && printf ok || printf bad'`, 30)
	require.NoError(t, err)
	asks := ask.asks()
	require.Len(t, asks, 2)
	require.Contains(t, asks[1].Questions[0].Text, "previous attempt was rejected")
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "ok", res.Output)
	require.NotContains(t, res.Output, "wrong-one")
	require.NotContains(t, res.Output, "open-sesame")
}

// An answer that the reader has not consumed yet does not re-open the
// dialog. The terminal looks exactly as it did when it asked - the job
// is still parked in a hidden-line read, still with echo off - until the
// reader is scheduled, which on a loaded machine can outlast the quiet
// window. Asking again there would ask the user for the same password a
// second time and call the first attempt rejected, and cancelling that
// second dialog kills the command.
func TestPtyRunner_UnconsumedAnswerDoesNotReopenDialog(t *testing.T) {
	r, ask := newAskRunner(t, "open-sesame")
	// Give the runner its sentinel; the fake terminal below stands in
	// for the session only for the wait.
	_, err := r.Type(t.Context(), "true", 10)
	require.NoError(t, err)

	stuck := &stuckReaderTerm{}
	res, err := r.awaitCompletion(t.Context(), stuck, nil, 2)
	require.NoError(t, err)
	require.True(t, res.Running)

	require.Len(t, ask.asks(), 1, "the unanswered read is the same prompt, not a retry")
	require.Equal(t, [][]byte{[]byte("open-sesame\n")}, stuck.writes(),
		"the answer went out once and nothing cancelled the reader")
}

// stuckReaderTerm is a terminal whose foreground job sits in a
// hidden-line read and never says anything: output is quiet from the
// start, and what is written to it is never read back. Ctrl-c is the
// one thing it reacts to - the reader it stands for would die - so a
// runner that cancels its way out of the wait ends the test instead of
// spinning in it.
type stuckReaderTerm struct {
	mu     sync.Mutex
	sent   [][]byte
	killed bool
}

func (s *stuckReaderTerm) Send(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(b) == 1 && b[0] == 0x03 {
		s.killed = true
		return nil
	}
	s.sent = append(s.sent, append([]byte(nil), b...))
	return nil
}

func (s *stuckReaderTerm) writes() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte{}, s.sent...)
}

func (s *stuckReaderTerm) reading() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.killed
}

func (s *stuckReaderTerm) WaitForAny(context.Context, []*regexp.Regexp, time.Duration) int {
	return -1
}

func (s *stuckReaderTerm) WaitForAnyOrQuiet(context.Context, []*regexp.Regexp, time.Duration, time.Duration) (int, bool) {
	return -1, true
}

func (s *stuckReaderTerm) WaitForOutput(context.Context, time.Duration) bool { return false }

func (s *stuckReaderTerm) WaitForQuiet(context.Context, time.Duration, time.Duration) bool {
	return true
}

func (s *stuckReaderTerm) Drain() []byte          { return nil }
func (s *stuckReaderTerm) Pending() []byte        { return nil }
func (s *stuckReaderTerm) PendingLen() int        { return 0 }
func (s *stuckReaderTerm) Alive() bool            { return true }
func (s *stuckReaderTerm) AltScreen() bool        { return false }
func (s *stuckReaderTerm) Screen() string         { return "" }
func (s *stuckReaderTerm) BracketedPaste() bool   { return false }
func (s *stuckReaderTerm) Paste(string) error     { return nil }
func (s *stuckReaderTerm) Size() (int, int)       { return term.DefaultSize() }
func (s *stuckReaderTerm) IdleFor() time.Duration { return time.Minute }

func (s *stuckReaderTerm) SampleJob() term.JobActivity {
	return term.JobActivity{
		Observed:      s.reading(),
		Asleep:        true,
		InputWait:     true,
		WchanReadable: true,
	}
}

func (s *stuckReaderTerm) SecretRead() term.SecretReadState {
	if !s.reading() {
		return term.SecretReadNo
	}
	return term.SecretReadYes
}

func (s *stuckReaderTerm) ResetWaitSample() {}
func (s *stuckReaderTerm) RescanFromStart() {}
func (s *stuckReaderTerm) Close()           {}

func TestPtyRunner_MultilineCommand(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "for i in 1 2 3\ndo echo $i\ndone", 10)
	require.NoError(t, err)
	// Multiline commands work like any other: the prompt marker comes
	// back when the whole thing has run.
	require.Contains(t, res.Output, "1")
	require.Contains(t, res.Output, "3")
}

func TestPtyRunner_LongOutputKeepsTail(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "seq 1 500", 15)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Contains(t, res.Output, "499")
	require.Contains(t, res.Output, "500")
}

func TestPtyRunner_EchoStripped(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "echo distinctivestring", 10)
	require.NoError(t, err)
	// The echoed command line is stripped; only the output remains.
	require.Equal(t, "distinctivestring", res.Output)
	require.Equal(t, 1, len(regexp.MustCompile("distinctivestring").FindAllString(res.Output, -1)))
}

func TestEchoDebris(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		line string
		echo string
		want bool
	}{
		{"exact", "echo hi", "echo hi", true},
		{"prompt prefix", "sh-5.3$ echo hi", "echo hi", true},
		// zle echoes the line, then the syntax-highlighting redisplay
		// reprints it on the same line.
		{"redraw, whole copy", "echo helloecho hello", "echo hello", true},
		{"redraw, partial copy", "echo helloecho", "echo hello", true},
		{"redraw, skipped runs", `which zsh; echo "SHELL=$SHELL"; ps -o comm= -p $PPIDwhich; echo "SHELL=$SHELL"; ps -o -p $PPID`, `which zsh; echo "SHELL=$SHELL"; ps -o comm= -p $PPID`, true},
		// Real output that merely echoes fragments of the command is kept.
		{"output shorter than command", "echo", "echo hello", false},
		{"output ending with sent text", "got:hello", "hello", false},
		{"output starting mid-command", "hello", "echo hello", false},
		{"repeated junk", "nananananananana", "banana", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, echoDebris(tc.line, tc.echo))
		})
	}
}

// The echo of a command line arrives a piece at a time, and a piece of
// it that says "password:" is not a program asking for one - so the
// half-arrived echo has to be recognised as the command it is, prompt
// and all, well before the whole line is on the wire.
func TestEchoStarted(t *testing.T) {
	t.Parallel()

	const cmd = `/bin/sh -c 'stty -echo; printf "Password: "; read a'`

	cases := []struct {
		name string
		line string
		want bool
	}{
		{"whole command", cmd, true},
		{"half arrived", `/bin/sh -c 'stty -echo; printf "Passw`, true},
		{"behind the prompt", "sh-5.3$ " + cmd, true},
		{"behind the prompt, half arrived", `sh-5.3$ /bin/sh -c 'stty -echo; printf "Password: `, true},
		// What a program prints is output, however much of the
		// command's own vocabulary it repeats.
		{"the prompt the command prints", "Password: ", false},
		{"output quoting the command", `read a' failed`, false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, echoStarted(tc.line, []string{cmd}))
		})
	}
}

// zshPromptSp is what zsh prints before every prompt when the
// partial-line marker (PROMPT_SP) is on: a highlighted %, a space fill
// to the last column, then carriage returns that erase it again. Stripped
// of escapes it frames real output with "%" plus whitespace, at the
// start (the prompt before the command, undrained) and the end (the
// prompt after it).
func zshPromptSp(fill int) string {
	return "\x1b[1m\x1b[7m%\x1b[27m\x1b[1m\x1b[0m" + strings.Repeat(" ", fill) +
		"\r \r\r\x1b[0m\x1b[27m\x1b[24m\x1b[J\x1b]133;A\a\x1b[K\x1b[?1h\x1b=\x1b[?2004h"
}

// TestPtyCleanZshStream runs clean() over raw zsh byte streams captured
// from a real session: zle echoes a character, backspaces over it and
// redisplays the command with per-character color codes and
// cursor-forward skips, and prompts wrap output in PROMPT_SP markers.
// The cleaned output must be the command's output alone.
func TestPtyCleanZshStream(t *testing.T) {
	t.Parallel()

	r := &ptyRunner{sentinel: newSentinel(posixDialect)}

	cases := []struct {
		name string
		raw  string
		echo []string
		want string
	}{
		{
			name: "echo",
			raw: zshPromptSp(215) +
				"e\becho hello\x1b[10D\x1b[36me\x1b[36mc\x1b[36mh\x1b[36mo\x1b[39m\x1b[6C" +
				"\x1b[?1l\x1b>\x1b[?2004l\r\r\nhello\r\n" + zshPromptSp(215) +
				r.sentinel.cmd + "\r\n",
			echo: []string{"echo hello"},
			want: "hello",
		},
		{
			name: "partial line",
			raw: "p\bprintf %s partial-output\x1b[24D\x1b[36mp\x1b[36mr\x1b[36mi\x1b[36mn\x1b[36mt\x1b[36mf\x1b[39m \x1b[33m%\x1b[33ms\x1b[39m\x1b[15C" +
				"\x1b[?1l\x1b>\x1b[?2004l\r\r\npartial-output" + zshPromptSp(215) +
				r.sentinel.cmd + "\r\n",
			echo: []string{"printf %s partial-output"},
			want: "partial-output",
		},
		{
			name: "long command",
			raw: "l\bls /home/x/internal/shell/\x1b[26D\x1b[36ml\x1b[36ms\x1b[39m \x1b[4m/\x1b[4mh\x1b[4mo\x1b[4mm\x1b[4me\x1b[24m\x1b[?1l\x1b>\x1b[?2004l\r\r\n" +
				"background.go    shell.go\r\nstream.go         run.go\r\n" + zshPromptSp(215) +
				r.sentinel.cmd + "\r\n",
			echo: []string{"ls /home/x/internal/shell/"},
			want: "background.go    shell.go\nstream.go         run.go",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, r.clean(tc.raw, tc.echo))
		})
	}
}

// TestPtyRunner_ZshUserShell runs commands through a real zsh with the
// user's rc files loaded: zle line editing, zsh-syntax-highlighting
// redisplay and the PROMPT_SP partial-line marker all echo far more than
// the command. The cleaned output must still be the command's output
// alone - no doubled command, no "%" framing, no filler whitespace.
func TestPtyRunner_ZshUserShell(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	t.Setenv("SHELL", "zsh")
	r := &ptyRunner{cwd: t.TempDir()}
	t.Cleanup(r.Close)
	_, err := r.terminal(t.Context())
	require.NoError(t, err)

	res, err := r.Type(t.Context(), "echo hello-zsh", 15)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "hello-zsh", res.Output)

	res, err = r.Type(t.Context(), "printf %s partial-zsh", 15)
	require.NoError(t, err)
	require.Equal(t, "partial-zsh", res.Output)
}

// A multiline command written straight into a line editor is mangled:
// zle echoes each line as it arrives, then redisplays it, toggling
// bracketed paste around every prompt - raw echo debris (blank lines,
// bells, doubled fragments) that survives cleaning. Commands must be
// delivered as a paste instead, so the shell takes the whole block at
// once. The issue's case: a heredoc whose body is many lines of code.
func TestPtyRunner_ZshUserShellHeredoc(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	t.Setenv("SHELL", "zsh")
	r := &ptyRunner{cwd: t.TempDir()}
	t.Cleanup(r.Close)
	_, err := r.terminal(t.Context())
	require.NoError(t, err)

	cmd := "cat > file.txt <<'EOF'\npackage main\n\nfunc main() {}\nEOF\ncat file.txt"
	res, err := r.Type(t.Context(), cmd, 15)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	// The file's content must round-trip intact: that is the paste-
	// delivery contract. How much zle echo debris survives cleaning
	// varies with the zsh version, so pin the body, not the exact
	// output.
	require.Contains(t, res.Output, "package main\n\nfunc main() {}",
		"a heredoc's body goes into the file, not the output")
}

func TestResolveBackspaces(t *testing.T) {
	t.Parallel()

	require.Equal(t, "echo hello", resolveBackspaces("e\becho hello"))
	require.Equal(t, "", resolveBackspaces("x\b"))
	require.Equal(t, "ün", resolveBackspaces("üü\bn"))
	require.Equal(t, "no backspaces", resolveBackspaces("no backspaces"))
}

func TestPtyCredPromptPattern(t *testing.T) {
	t.Parallel()

	match := []string{
		"[sudo] password for stubbe: ",
		"[sudo] Passwort für stubbe: ",
		"[sudo] Mot de passe de stubbe : ",
		"sudo password for root: ",
		"Password: ",
		"Current password: ",
		"Enter passphrase for key '/home/u/.ssh/id_ed25519': ",
		"PIN: ",
	}
	for _, s := range match {
		require.True(t, credPromptRe.MatchString(s), "should match %q", s)
	}
	noMatch := []string{
		"the password is hunter2", // a value, not a prompt
		"passwordless login enabled",
		"pinning: true",
		// A rejection is not a question: the reader prints it before it
		// re-prompts, and the prompt it prints next is what opens the
		// dialog (see TestPtyRunner_WrongPasswordReopensMaskedDialog).
		"Sorry, try again.",
	}
	for _, s := range noMatch {
		require.False(t, credPromptRe.MatchString(s), "should not match %q", s)
	}
	// Ordinary output that merely looks prompt-shaped matches the hint;
	// the echo-bit confirmation is what keeps it from being intercepted
	// (see TestPtyRunner_PlainQuestionIsNotMasked).
	require.True(t, credPromptRe.MatchString("some password for fun: "))
}

func TestPtySentinelParsing(t *testing.T) {
	t.Parallel()

	s := newSentinel(posixDialect)
	tag := strings.TrimSuffix(strings.TrimPrefix(s.cmd, "printf '__exit_"), `:%d@%s__' "$?" "$PWD"`)
	require.Len(t, tag, 16)

	require.True(t, s.loose.MatchString("__exit_"+tag+":0@"))
	require.True(t, s.parse.MatchString("__exit_"+tag+":12@/tmp/x__"))
	m := s.parse.FindStringSubmatch("__exit_" + tag + ":-1@/home__")
	require.Equal(t, "-1", m[1])
	require.Equal(t, "/home", m[2])

	// The echoed sentinel command must not match (format specifiers).
	require.False(t, s.parse.MatchString(s.cmd))
	require.False(t, s.loose.MatchString(s.cmd))

	// A marker printed by something else - another session, a log line -
	// is not this session's answer.
	other := newSentinel(posixDialect)
	require.False(t, s.parse.MatchString("__exit_deadbeefdeadbeef:0@/tmp__"))
	require.False(t, s.parse.MatchString(other.cmd))
	require.NotEqual(t, s.cmd, other.cmd)
}

func TestPtyRunnerIdleReapAndCap(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	t.Setenv("SHELL", "/bin/sh")

	ptyRunnersMu.Lock()
	saved := ptyRunners
	ptyRunners = map[string]*ptyRunner{}
	ptyRunnersMu.Unlock()
	t.Cleanup(func() {
		ptyRunnersMu.Lock()
		for _, r := range ptyRunners {
			r.Close()
		}
		ptyRunners = saved
		ptyRunnersMu.Unlock()
	})

	// Fill to the cap.
	for i := range ptyMaxRunners {
		dir := t.TempDir()
		r := ptyRunnerFor("test", "", dir, nil)
		if _, err := r.terminal(t.Context()); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	ptyRunnersMu.Lock()
	require.Len(t, ptyRunners, ptyMaxRunners)
	ptyRunnersMu.Unlock()

	// One more evicts the most idle.
	time.Sleep(10 * time.Millisecond)
	r := ptyRunnerFor("test", "", t.TempDir(), nil)
	_, err := r.terminal(t.Context())
	require.NoError(t, err)
	ptyRunnersMu.Lock()
	require.Len(t, ptyRunners, ptyMaxRunners)
	ptyRunnersMu.Unlock()

	// An idle-beyond-timeout runner is reaped on the next sweep.
	ptyRunnersMu.Lock()
	var victim *ptyRunner
	for _, cand := range ptyRunners {
		if cand != r {
			victim = cand
			break
		}
	}
	require.NotNil(t, victim)
	victim.mu.Lock()
	victim.lastUsed = time.Now().Add(-ptyIdleTimeout - time.Minute)
	victim.mu.Unlock()
	ptyRunnersMu.Unlock()

	ptyRunnersMu.Lock()
	ptyReap()
	ptyRunnersMu.Unlock()

	ptyRunnersMu.Lock()
	defer ptyRunnersMu.Unlock()
	require.Len(t, ptyRunners, ptyMaxRunners-1)
	_, stillThere := ptyRunners[runnerKey("test", r.cwd)]
	require.True(t, stillThere, "the just-used runner must survive the reap")
}

// Output a call leaves behind belongs to that call. A backgrounded job
// that prints after its call returned, or a prompt the previous command
// left in the buffer, must not surface in the next command's result:
// before each command the session is fenced down to a known point.
func TestPtyRunner_EarlierOutputStaysOutOfTheNextCall(t *testing.T) {
	r := newTestRunner(t)

	_, err := r.Type(t.Context(), "(sleep 1; echo LATE) &", 10)
	require.NoError(t, err)

	// Let the backgrounded job print into the session while no call is
	// waiting on it.
	time.Sleep(1500 * time.Millisecond)

	res, err := r.Type(t.Context(), "echo second", 10)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "second", res.Output, "a command reports its own output and nothing else")
}

// Each owner (agent) gets its own set of sessions: the same directory
// under two owners yields two independent runners, and re-resolving
// one owner's primary returns the same runner again.
func TestPtyRunner_OwnersGetSeparateSessions(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}

	ptyRunnersMu.Lock()
	saved := ptyRunners
	ptyRunners = map[string]*ptyRunner{}
	ptyRunnersMu.Unlock()
	t.Cleanup(func() {
		ptyRunnersMu.Lock()
		for _, r := range ptyRunners {
			r.Close()
		}
		ptyRunners = saved
		ptyRunnersMu.Unlock()
	})

	a := ptyRunnerFor("agent-a", "", t.TempDir(), nil)
	b := ptyRunnerFor("agent-b", "", a.cwd, nil)
	require.NotSame(t, a, b, "two owners in one directory must not share a session")
	require.Same(t, a, ptyRunnerFor("agent-a", "", a.cwd, nil), "one owner resolves back to its own runner")
}

// Concurrent dispatches of one agent type share no terminal: the
// session ID from the tool context (unique per agent-tool dispatch)
// keys the runner, so a sibling's command cannot be typed into another
// dispatch's running program. Cleanup still closes every session the
// agent holds, and no agent's ID can close another's (exact match, not
// prefix: "fast" must not close "fast2").
func TestPtyRunner_DispatchesGetSeparateSessions(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}

	ptyRunnersMu.Lock()
	saved := ptyRunners
	ptyRunners = map[string]*ptyRunner{}
	ptyRunnersMu.Unlock()
	t.Cleanup(func() {
		ptyRunnersMu.Lock()
		for _, r := range ptyRunners {
			r.Close()
		}
		ptyRunners = saved
		ptyRunnersMu.Unlock()
	})

	cwd := t.TempDir()
	d1 := ptyRunnerFor("fast", "msg1$$call1", cwd, nil)
	d2 := ptyRunnerFor("fast", "msg2$$call2", cwd, nil)
	require.NotSame(t, d1, d2, "two dispatches of one agent must not share a terminal")
	require.Same(t, d1, ptyRunnerFor("fast", "msg1$$call1", cwd, nil), "one dispatch resolves back to its own runner")

	ptyRunnersMu.Lock()
	require.Len(t, ptyRunners, 2)
	d2key := d2.key
	ptyRunnersMu.Unlock()

	// A same-prefixed sibling agent is untouched by fast's cleanup.
	other := ptyRunnerFor("fast2", "", cwd, nil)
	closeOwnerSessions("fast")
	ptyRunnersMu.Lock()
	_, d2Gone := ptyRunners[d2key]
	_, otherAlive := ptyRunners[other.key]
	require.False(t, d2Gone, "fast's dispatch sessions must be closed with the agent")
	require.True(t, otherAlive, "fast2 must keep its sessions when fast closes its own")
	for _, r := range ptyRunners {
		r.Close()
	}
	ptyRunnersMu.Unlock()
}

// Reset is the way out of a session that cannot be talked down: the
// shell is killed and a fresh one takes its place, so everything the
// old one held is gone and commands work again.
func TestPtyRunner_ResetStartsAFreshShell(t *testing.T) {
	r := newTestRunner(t)
	_, err := r.Type(t.Context(), "export PTY_RESET_VAR=before", 10)
	require.NoError(t, err)

	require.NoError(t, r.Reset(t.Context()))

	res, err := r.Type(t.Context(), "printf %s \"$PTY_RESET_VAR\"", 10)
	require.NoError(t, err)
	require.Equal(t, "", res.Output, "the new shell has none of the old one's state")

	alive, err := r.Type(t.Context(), "echo alive", 10)
	require.NoError(t, err)
	require.Equal(t, "alive", alive.Output)
	require.NotNil(t, alive.ExitCode)
	require.Equal(t, 0, *alive.ExitCode)
}

// A command left running does not survive a reset: killing the shell is
// the point, and the session comes back usable rather than busy.
func TestPtyRunner_ResetClearsARunningCommand(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "sleep 30", 1)
	require.NoError(t, err)
	require.True(t, res.Running)

	require.NoError(t, r.Reset(t.Context()))
	require.True(t, r.shellIdle(r.session), "a reset session is free")

	done, err := r.Type(t.Context(), "echo back", 10)
	require.NoError(t, err)
	require.Equal(t, "back", done.Output)
}

// A shell without a line editor - dash, or bash built without readline -
// sends no bracketed-paste marker before its prompt, so the only thing
// separating a command's output from the prompt behind it is the prompt
// marker itself. Output that did not end in a newline must survive
// that: before the marker was split on, it glued onto the echoed exit
// sentinel, matched the sentinel, and was dropped with it.
func TestPtyRunner_CleanKeepsUnterminatedOutputWithoutBracketedPaste(t *testing.T) {
	r := &ptyRunner{cwd: t.TempDir()}
	r.sentinel = newSentinel(posixDialect)
	r.promptRe = ptyPromptRe

	cmd := `printf %s "$PTY_TEST_VAR"`
	// What a paste-less shell puts on the wire: the echoed command, the
	// output with no trailing newline, the prompt marker, then the echo
	// of the sentinel and its answer.
	// collectResult cuts the sentinel answer off before cleaning, so
	// what arrives here ends with the echo of the sentinel command.
	raw := cmd + "\r\npersisted" + "\x1b]133;A\x07" + r.sentinel.cmd + "\r\n"

	require.Equal(t, "persisted", r.clean(raw, []string{cmd}))
}

func TestPtyRunner_CleanDropsPromptAndSentinelLines(t *testing.T) {
	r := &ptyRunner{cwd: t.TempDir()}
	r.sentinel = newSentinel(posixDialect)
	r.promptRe = ptyPromptRe

	cmd := "echo hello"
	raw := cmd + "\r\nhello\r\n\x1b]133;A\x07" + r.sentinel.cmd + "\r\n" +
		"\x1b]133;A\x07" + r.sentinel.begin + "\r\n"

	require.Equal(t, "hello", r.clean(raw, []string{cmd}),
		"prompt markers, the exit sentinel and the fence are bookkeeping, not output")
}

// TestPtyRunner_CwdIfMoved pins the rule the shell response relies on: the
// working directory is announced when it changes and stays silent when it
// does not, so a persistent session does not restate its own state on every
// call.
func TestPtyRunner_CwdIfMoved(t *testing.T) {
	t.Parallel()

	r := &ptyRunner{cwd: "/repo"}

	require.Empty(t, r.cwdIfMoved("/repo"), "the directory the session opened in is already known")
	require.Empty(t, r.cwdIfMoved(""), "a call with no sentinel announces nothing")
	require.Equal(t, "/repo/sub", r.cwdIfMoved("/repo/sub"), "a move is announced")
	require.Empty(t, r.cwdIfMoved("/repo/sub"), "staying put is not announced again")
	require.Equal(t, "/repo", r.cwdIfMoved("/repo"), "moving back is a move too")
}
