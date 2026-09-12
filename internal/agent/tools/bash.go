package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/shell"
	"github.com/stubbedev/harness/internal/term"
)

type BashParams struct {
	Description         string `json:"description" description:"A brief description of what the command does, try to keep it under 30 characters or so"`
	Command             string `json:"command" description:"The command to run in the persistent terminal session. Leave empty with input set to send keystrokes to a running program, or both empty to poll its output."`
	Input               string `json:"input,omitempty" description:"Raw keystrokes to send to the terminal (text, answers to prompts, key sequences for the running program). Append \\n to submit a line. Use instead of command when something interactive is already running."`
	Keys                string `json:"keys,omitempty" description:"Named keys to send instead of raw text, comma separated in order (e.g. \"ctrl+c\" or \"escape, :, w, q, enter\"). Supported: enter, tab, backtab, escape, space, backspace, delete, up, down, left, right, home, end, pageup, pagedown, insert, f1-f12, any ctrl+<letter>, and single characters typed literally."`
	Resize              string `json:"resize,omitempty" description:"Resize the terminal as COLSxROWS (e.g. \"240x60\") and return the redrawn screen. Use when a full-screen program needs more room; the size sticks for the whole session."`
	WorkingDir          string `json:"working_dir,omitempty" description:"The working directory the terminal session was opened in; the session itself tracks cd"`
	RunInBackground     bool   `json:"run_in_background,omitempty" description:"Set to true (boolean) to run this command in a detached background shell. Use job_output to read the output later. Prefer this only for servers and watchers; everything else belongs in the terminal session."`
	AutoBackgroundAfter int    `json:"auto_background_after,omitempty" description:"Seconds to wait once the command goes idle before returning it as still running (default: 60). Output, CPU or memory activity keeps the wait going, so this only ends a call that has genuinely stalled; hard ceiling 15 minutes"`
}

type BashResponseMetadata struct {
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	Output           string `json:"output"`
	Description      string `json:"description"`
	WorkingDirectory string `json:"working_directory"`
	Background       bool   `json:"background,omitempty"`
	ShellID          string `json:"shell_id,omitempty"`
}

const (
	BashToolName = "bash"

	DefaultAutoBackgroundAfter = 60 // Commands taking longer automatically become background jobs
	MaxOutputLength            = 30000
	BashNoOutput               = "no output"

	// backgroundStartGrace is how long a command started in the background is
	// given to fail fast before the tool reports it as running. It is a
	// ceiling, not a delay: a command that exits sooner is reported as soon
	// as it does.
	backgroundStartGrace = time.Second
)

//go:embed bash.md.tpl
var bashDescriptionTmpl []byte

var bashDescriptionTpl = template.Must(
	template.New("bashDescription").
		Parse(string(bashDescriptionTmpl)),
)

type bashDescriptionData struct {
	BannedCommands  string
	MaxOutputLength int
	Attribution     config.Attribution
	ModelID         string
	RgAvailable     bool
	GhAvailable     bool
	DefaultRows     int
	DefaultCols     int
}

var bannedCommands = []string{
	// Network/Download tools
	"alias",
	"aria2c",
	"axel",
	"chrome",
	"curl",
	"curlie",
	"firefox",
	"http-prompt",
	"httpie",
	"links",
	"lynx",
	"nc",
	"safari",
	"scp",
	"ssh",
	"telnet",
	"w3m",
	"wget",
	"xh",

	// System administration
	"doas",
	"su",
	"sudo",

	// Package managers
	"apk",
	"apt",
	"apt-cache",
	"apt-get",
	"dnf",
	"dpkg",
	"emerge",
	"home-manager",
	"makepkg",
	"opkg",
	"pacman",
	"paru",
	"pkg",
	"pkg_add",
	"pkg_delete",
	"portage",
	"rpm",
	"yay",
	"yum",
	"zypper",

	// System modification
	"at",
	"batch",
	"chkconfig",
	"crontab",
	"fdisk",
	"mkfs",
	"mount",
	"parted",
	"service",
	"systemctl",
	"umount",

	// Network configuration
	"firewall-cmd",
	"ifconfig",
	"ip",
	"iptables",
	"netstat",
	"pfctl",
	"route",
	"ufw",
}

func bashDescription(attribution *config.Attribution, modelID string) string {
	bannedCommandsStr := strings.Join(bannedCommands, ", ")
	descRows, descCols := term.DefaultSize()
	var out bytes.Buffer
	if err := bashDescriptionTpl.Execute(&out, bashDescriptionData{
		BannedCommands:  bannedCommandsStr,
		MaxOutputLength: MaxOutputLength,
		Attribution:     *attribution,
		ModelID:         modelID,
		RgAvailable:     getRg() != "",
		GhAvailable:     ghAvailable,
		DefaultRows:     descRows,
		DefaultCols:     descCols,
	}); err != nil {
		// this should never happen.
		panic("failed to execute bash description template: " + err.Error())
	}
	return out.String()
}

func blockFuncs() []shell.BlockFunc {
	return []shell.BlockFunc{
		shell.CommandsBlocker(bannedCommands),

		// System package managers
		shell.ArgumentsBlocker("apk", []string{"add"}, nil),
		shell.ArgumentsBlocker("apt", []string{"install"}, nil),
		shell.ArgumentsBlocker("apt-get", []string{"install"}, nil),
		shell.ArgumentsBlocker("dnf", []string{"install"}, nil),
		shell.ArgumentsBlocker("pacman", nil, []string{"-S"}),
		shell.ArgumentsBlocker("pkg", []string{"install"}, nil),
		shell.ArgumentsBlocker("yum", []string{"install"}, nil),
		shell.ArgumentsBlocker("zypper", []string{"install"}, nil),

		// Language-specific package managers
		shell.ArgumentsBlocker("brew", []string{"install"}, nil),
		shell.ArgumentsBlocker("cargo", []string{"install"}, nil),
		shell.ArgumentsBlocker("gem", []string{"install"}, nil),
		shell.ArgumentsBlocker("go", []string{"install"}, nil),
		shell.ArgumentsBlocker("npm", []string{"install"}, []string{"--global"}),
		shell.ArgumentsBlocker("npm", []string{"install"}, []string{"-g"}),
		shell.ArgumentsBlocker("pip", []string{"install"}, []string{"--user"}),
		shell.ArgumentsBlocker("pip3", []string{"install"}, []string{"--user"}),
		shell.ArgumentsBlocker("pnpm", []string{"add"}, []string{"--global"}),
		shell.ArgumentsBlocker("pnpm", []string{"add"}, []string{"-g"}),
		shell.ArgumentsBlocker("yarn", []string{"global", "add"}, nil),

		// `go test -exec` can run arbitrary commands
		shell.ArgumentsBlocker("go", []string{"test"}, []string{"-exec"}),
	}
}

func NewBashTool(workingDir string, attribution *config.Attribution, modelID string, questions question.Service) fantasy.AgentTool {
	// The synchronous execution path runs in persistent terminal
	// sessions (see pty.go): a real PTY whose shell state and sudo
	// credential survive across calls, with a second session opened on
	// demand when the first is busy driving an editor or TUI. The
	// primary one warms up in the background while the agent starts.
	_ = ptyRunnerFor(workingDir, questions)
	return fantasy.NewAgentTool(
		BashToolName,
		string(bashDescription(attribution, modelID)),
		func(ctx context.Context, params BashParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			// Determine working directory
			execWorkingDir := cmp.Or(params.WorkingDir, workingDir)

			// If explicitly requested as background, start immediately with detached context
			if params.RunInBackground {
				startTime := time.Now()
				bgManager := shell.GetBackgroundShellManager()
				bgManager.Cleanup()
				// Use background context so it continues after tool returns
				bgShell, err := bgManager.Start(context.Background(), execWorkingDir, blockFuncs(), params.Command, params.Description)
				if err != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("error starting background shell: %w", err)
				}

				// Wait a short time to detect fast failures (blocked commands,
				// syntax errors, etc.), returning the moment the command exits
				// instead of always burning the whole grace period.
				graceCtx, cancelGrace := context.WithTimeout(ctx, backgroundStartGrace)
				bgShell.WaitContext(graceCtx)
				cancelGrace()

				stdout, stderr, done, execErr := bgShell.GetOutput()

				if done {
					// Command failed or completed very quickly
					bgManager.Remove(bgShell.ID)

					interrupted := shell.IsInterrupt(execErr)
					exitCode := shell.ExitCode(execErr)
					if exitCode == 0 && !interrupted && execErr != nil {
						return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
					}

					stdout = formatOutput(stdout, stderr, execErr)

					metadata := BashResponseMetadata{
						StartTime:        startTime.UnixMilli(),
						EndTime:          time.Now().UnixMilli(),
						Output:           stdout,
						Description:      params.Description,
						Background:       params.RunInBackground,
						WorkingDirectory: bgShell.WorkingDir,
					}
					if stdout == "" {
						return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
					}
					stdout += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(bgShell.WorkingDir))
					return fantasy.WithResponseMetadata(fantasy.NewTextResponse(stdout), metadata), nil
				}

				// Still running after fast-failure check - return as background job
				metadata := BashResponseMetadata{
					StartTime:        startTime.UnixMilli(),
					EndTime:          time.Now().UnixMilli(),
					Description:      params.Description,
					WorkingDirectory: bgShell.WorkingDir,
					Background:       true,
					ShellID:          bgShell.ID,
				}
				response := fmt.Sprintf("Background shell started with ID: %s\n\nUse job_output tool to view output or job_kill to terminate.", bgShell.ID)
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
			}

			// Everything synchronous goes through the persistent terminal
			// session: a command runs and reports its exit code, input
			// drives whatever is running, and an empty call polls.
			startTime := time.Now()
			waitSeconds := cmp.Or(params.AutoBackgroundAfter, DefaultAutoBackgroundAfter)

			// Which terminal session serves this call: keystrokes go to
			// the one with a program in it, commands to one that is free
			// - a second shell is opened when an editor or TUI is still
			// holding the first.
			var result PTYResult
			var err error
			session := ptyInteractiveRunner(execWorkingDir, questions)
			switch {
			case params.Resize != "":
				var rows, cols int
				if rows, cols, err = parseTerminalSize(params.Resize); err == nil {
					result, err = session.Resize(ctx, rows, cols)
				}
			case params.Keys != "":
				result, err = session.Keys(ctx, params.Keys)
			case params.Input != "":
				result, err = session.Input(ctx, params.Input)
			case params.Command != "":
				if session, err = ptyCommandRunner(ctx, execWorkingDir, questions); err == nil {
					result, err = session.Run(ctx, params.Command, waitSeconds)
				}
			default:
				result, err = session.Poll(ctx)
			}
			switch {
			case errors.Is(err, errAltScreenBusy), errors.Is(err, errAllSessionsBusy):
				// The model's mistake, not a tool failure: tell it what
				// is in the way and how to get past it.
				return fantasy.NewTextErrorResponse(err.Error()), nil
			case err != nil:
				return fantasy.ToolResponse{}, fmt.Errorf("terminal session: %w", err)
			}

			stdout := TruncateOutput(result.Output)

			var header string
			switch {
			case result.Interrupted:
				header = "Interrupted: the command was stopped with ctrl+c because the call was cancelled."
			case result.WhileBusy:
				header = "A command is still running in this terminal session. Below is its screen as it stands; the command's own output goes to the call waiting on it. Keystrokes you send here reach that command."
			case result.Waiting:
				header = "The command has stopped and is waiting for input (no exit code yet). Send input or keys to answer it - below is what it printed before stopping - or ctrl+c (keys) to give up on it."
			case result.AltScreen && result.Unchanged:
				header = "A full-screen program owns the terminal; its screen is unchanged since the last call. Drive it with keys/input, or send ctrl+c (keys) to stop it."
			case result.AltScreen:
				rows, cols := session.Size()
				header = fmt.Sprintf("A full-screen program owns the terminal. Below is its rendered %dx%d screen, not a stream of output; there is no exit code until it quits. Drive it with keys/input, poll to see it again, or send ctrl+c (keys) to stop it.", cols, rows)
			case result.Running:
				header = "Still running in the terminal session (no exit code yet) but making no measurable progress - no output, no CPU, no memory change. Send input or keys to interact with it, poll (empty call) to wait for it to finish, or ctrl+c (keys) to stop it."
			case result.ExitCode != nil && *result.ExitCode != 0:
				header = fmt.Sprintf("Exit code %d", *result.ExitCode)
			case params.Resize != "":
				rows, cols := session.Size()
				header = fmt.Sprintf("Terminal resized to %dx%d.", cols, rows)
			case params.Keys != "":
				header = "Keys sent."
			case params.Input != "":
				header = "Input sent."
			case result.Output == "" && params.Command == "":
				header = "No new output."
			}

			metadata := BashResponseMetadata{
				StartTime:        startTime.UnixMilli(),
				EndTime:          time.Now().UnixMilli(),
				Output:           stdout,
				Description:      params.Description,
				WorkingDirectory: cmp.Or(result.Cwd, execWorkingDir),
			}

			var sb strings.Builder
			if session.slot > 0 {
				fmt.Fprintf(&sb,
					"Ran in terminal session #%d: session #1 is busy with an interactive program. "+
						"This is a separate shell - it does not have the other one's cd, exported "+
						"variables or activated environments.\n", session.slot+1)
			}
			if session.tookRestart() {
				sb.WriteString("The terminal session's shell had exited, so a new one was started: " +
					"working directory, exported variables, activated environments and the sudo " +
					"credential from before are gone.\n")
			}
			if header != "" {
				sb.WriteString(header + "\n")
			}
			if stdout != "" {
				sb.WriteString(stdout)
				fmt.Fprintf(&sb, "\n\n<cwd>%s</cwd>", normalizeWorkingDir(cmp.Or(result.Cwd, execWorkingDir)))
			} else if header == "" {
				sb.WriteString(BashNoOutput)
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(sb.String()), metadata), nil
		},
	)
}

// formatOutput formats the output of a completed command with error handling
func formatOutput(stdout, stderr string, execErr error) string {
	interrupted := shell.IsInterrupt(execErr)
	exitCode := shell.ExitCode(execErr)

	stdout = truncateOutput(stdout)
	stderr = truncateOutput(stderr)

	errorMessage := stderr
	if errorMessage == "" && execErr != nil {
		errorMessage = execErr.Error()
	}

	if interrupted {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += "Command was aborted before completion"
	} else if exitCode != 0 {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += fmt.Sprintf("Exit code %d", exitCode)
	}

	hasBothOutputs := stdout != "" && stderr != ""

	if hasBothOutputs {
		stdout += "\n"
	}

	if errorMessage != "" {
		stdout += "\n" + errorMessage
	}

	return stdout
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

func truncateOutput(content string) string {
	return TruncateOutput(content)
}

func normalizeWorkingDir(path string) string {
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, fsext.WindowsWorkingDirDrive(), "")
	}
	return filepath.ToSlash(path)
}
