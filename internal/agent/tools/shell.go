package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/term"
)

type ShellParams struct {
	// Nothing here is required: a poll is an empty call. A schema that
	// demanded a command would reject the very call this tool's own
	// description asks for.
	Command             string `json:"command,omitempty" description:"What to type into the terminal: a command line at the prompt, or text and keys for the program that is running. Named keys go in angle brackets (<enter>, <escape>, <ctrl+c>). Leave empty to wait for the running command's next event."`
	Reset               bool   `json:"reset,omitempty" description:"Kill a wedged session and start a fresh one, losing everything the old shell held"`
	WorkingDir          string `json:"working_dir,omitempty" description:"Directory to open the session in; the session tracks cd from then on"`
	AutoBackgroundAfter int    `json:"auto_background_after,omitempty" description:"Seconds to hold a command that has gone completely idle before returning it as still running (default 60, ceiling 15 minutes)"`
}

type ShellResponseMetadata struct {
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	Output           string `json:"output"`
	Description      string `json:"description"`
	WorkingDirectory string `json:"working_directory"`
}

const (
	// ShellToolName is the terminal-session tool's name: it runs the
	// user's own shell, whatever that is, not bash specifically.
	ShellToolName = "shell"

	DefaultAutoBackgroundAfter = 60 // Seconds an idle command is held before it is reported as still running
	MaxOutputLength            = 30000
	ShellNoOutput              = "no output"
)

//go:embed shell.md.tpl
var shellDescriptionTmpl []byte

var shellDescriptionTpl = template.Must(
	template.New("shellDescription").
		Parse(string(shellDescriptionTmpl)),
)

type shellDescriptionData struct {
	MaxOutputLength int
	ModernTools     string
	DefaultRows     int
	DefaultCols     int
	Shell           string
}

func shellDescription(shell string) string {
	descRows, descCols := term.DefaultSize()
	var out bytes.Buffer
	if err := shellDescriptionTpl.Execute(&out, shellDescriptionData{
		MaxOutputLength: MaxOutputLength,
		ModernTools:     availableModernTools(),
		DefaultRows:     descRows,
		DefaultCols:     descCols,
		// Naming the shell is most of what the model needs from this
		// description: it already knows how to write for bash, zsh or
		// PowerShell, but not which one it has. The tool is only built
		// when one was identified, so there is always an answer here.
		Shell: filepath.Base(shell),
	}); err != nil {
		// this should never happen.
		panic("failed to execute shell description template: " + err.Error())
	}
	return out.String()
}

// conflictingShellInputs reports, as a message for the model, when one
// call asks for two different things at once: a command to type and a
// session to kill. There is no order in which honouring both is what
// the caller meant. It returns the empty string when the call is
// unambiguous.
func conflictingShellInputs(p ShellParams) string {
	var asked []string
	if p.Command != "" {
		asked = append(asked, "command")
	}
	if p.Reset {
		asked = append(asked, "reset")
	}
	if len(asked) < 2 {
		return ""
	}
	return fmt.Sprintf("set %s in one call; send one per call, %s first",
		strings.Join(asked, " and "), asked[0])
}

// shellLabel is what the call is called in the UI and in the message
// metadata: the first line of what was typed, or a name for the calls
// that type nothing - a poll, a reset. The model is not asked to
// describe a call; the command line describes itself.
func shellLabel(p ShellParams) string {
	switch {
	case p.Reset:
		return "reset terminal session"
	case p.Command != "":
		return firstLine(p.Command)
	default:
		return "poll terminal session"
	}
}

// firstLine is a one-line stand-in for a command with no description:
// its first line, shortened to something that fits a label.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	const max = 60
	// Counted in runes, not bytes: a label cut mid-rune renders as a
	// replacement character.
	if runes := []rune(line); len(runes) > max {
		return string(runes[:max-1]) + "…"
	}
	return line
}

// NewShellTool builds the shell tool, or returns nil when no shell
// could be identified to run. A terminal nobody can name is not a
// terminal the model should be told it has: the description would have
// to lie about which shell it is writing for, and the session would open
// against a guess or not at all. ShellAvailable answers the same
// question without building anything.
func NewShellTool(workingDir, owner string, questions question.Service) fantasy.AgentTool {
	shellPath, ok := term.Shell()
	if !ok {
		return nil
	}
	// Commands run in a persistent terminal session (see pty.go): a real
	// PTY whose shell state and sudo credential survive across calls. It
	// warms up in the background while the agent starts.
	// Sessions are scoped to the owner - the agent's ID - so every
	// agent, the coder and each subagent alike, drives its own shell
	// and none of them can type into, reset or pollute another's.
	_ = ptyRunnerFor(owner, workingDir, questions)
	return fantasy.NewAgentTool(
		ShellToolName,
		string(shellDescription(shellPath)),
		func(ctx context.Context, params ShellParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			// Determine working directory
			execWorkingDir := cmp.Or(params.WorkingDir, workingDir)

			// Everything synchronous goes through the persistent terminal
			// session, used the way a person uses one: whatever is in
			// command is typed at it - a command line when the shell is
			// at its prompt, text and keys for the program when one is
			// running - and an empty call waits for the next thing to
			// happen. The session decides which of those it is.
			if conflict := conflictingShellInputs(params); conflict != "" {
				return fantasy.NewTextErrorResponse(conflict), nil
			}

			startTime := time.Now()
			waitSeconds := cmp.Or(params.AutoBackgroundAfter, DefaultAutoBackgroundAfter)

			var result PTYResult
			var err error
			session := ptyRunnerFor(owner, execWorkingDir, questions)
			switch {
			case params.Reset:
				err = session.Reset(ctx)
			case params.Command != "":
				result, err = session.Type(ctx, params.Command, waitSeconds)
			default:
				result, err = session.Poll(ctx)
			}
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("terminal session: %w", err)
			}

			stdout := TruncateOutput(result.Output)

			// The header names the state the call ended in and nothing
			// else. What to do about each state belongs to the model,
			// which knows how terminals work; repeating it on every
			// call would spend context on advice it did not ask for.
			var header string
			switch {
			case result.Interrupted:
				header = "[interrupted]"
			case result.WhileBusy:
				header = "[another command holds the session; its screen follows]"
			case result.Waiting:
				header = "[waiting for input]"
			case result.AltScreen && result.Unchanged:
				header = "[full-screen program; screen unchanged]"
			case result.AltScreen:
				rows, cols := session.Size()
				header = fmt.Sprintf("[full-screen program; %dx%d screen follows]", cols, rows)
			case result.Running:
				header = "[still running, idle]"
			case result.ExitCode != nil && *result.ExitCode != 0:
				header = fmt.Sprintf("[exit %d]", *result.ExitCode)
			case params.Reset:
				header = "[session reset]"
			case result.Output == "" && params.Command == "":
				header = "[no new output]"
			}

			// The best knowledge of where the session is: the last
			// command's sentinel, else the last completed command on
			// this session, else the directory it was opened in.
			cwd := cmp.Or(result.Cwd, session.knownCwd(), execWorkingDir)

			metadata := ShellResponseMetadata{
				StartTime:        startTime.UnixMilli(),
				EndTime:          time.Now().UnixMilli(),
				Output:           stdout,
				Description:      shellLabel(params),
				WorkingDirectory: cwd,
			}

			var sb strings.Builder
			// This says the shell state the model was counting on is
			// not the shell state it got, which it cannot work out from
			// the output alone. Everything else it can.
			if session.tookRestart() {
				sb.WriteString("[shell had exited; a fresh one replaced it and kept none of its state]\n")
			}
			if header != "" {
				sb.WriteString(header)
				sb.WriteString("\n")
			}
			if stdout != "" {
				sb.WriteString(stdout)
			} else if header == "" {
				sb.WriteString(ShellNoOutput)
			}
			// Only when the shell actually moved: see ptyRunner.cwdIfMoved.
			if moved := session.cwdIfMoved(cwd); moved != "" {
				fmt.Fprintf(&sb, "\n\n<cwd>%s</cwd>", normalizeWorkingDir(moved))
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(sb.String()), metadata), nil
		},
	)
}

func TruncateOutput(content string) string {
	if ansi.StringWidth(content) <= MaxOutputLength {
		return content
	}

	// Keep a short head and a long tail. What went wrong is almost
	// always at the end - the failing test, the compiler error, the
	// stack trace - while the head is usually preamble, so splitting the
	// budget evenly spends half of it on lines nobody needs.
	headLength := MaxOutputLength / 8
	start := ansi.Truncate(content, headLength, "")
	end := ansi.TruncateLeft(content, ansi.StringWidth(content)-(MaxOutputLength-headLength), "")

	truncatedLinesCount := max(strings.Count(content, "\n")-strings.Count(start, "\n")-strings.Count(end, "\n"), 0)
	return fmt.Sprintf("%s\n\n... [%d lines truncated] ...\n\n%s", start, truncatedLinesCount, end)
}

func normalizeWorkingDir(path string) string {
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, fsext.WindowsWorkingDirDrive(), "")
	}
	return filepath.ToSlash(path)
}

// ShellAvailable reports whether a shell could be identified to run a
// terminal session in. Callers assembling a tool set use it to leave
// the shell tool out rather than advertising one that cannot open.
func ShellAvailable() bool {
	_, ok := term.Shell()
	return ok
}
