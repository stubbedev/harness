package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

// stdoutIsTTY reports whether stdout is attached to a terminal. Single
// source for the TTY/plain-output decisions across the commands, so no
// command mixes detectors (models used go-isatty while everything else
// used charmbracelet/x/term).
func stdoutIsTTY() bool {
	return term.IsTerminal(os.Stdout.Fd())
}

// stdoutWidth returns the terminal width of stdout, falling back when
// stdout is not a terminal or the size cannot be determined.
func stdoutWidth(fallback int) int {
	if tw, _, err := term.GetSize(os.Stdout.Fd()); err == nil && tw > 0 {
		return tw
	}
	return fallback
}

// newJSONEncoder returns a JSON encoder that does not escape HTML, the
// convention for every --json output in this package.
func newJSONEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

func stdinIsTTY() bool {
	return term.IsTerminal(os.Stdin.Fd())
}

func confirmPrompt(in io.Reader, out io.Writer, prompt string) bool {
	_, _ = fmt.Fprint(out, prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
