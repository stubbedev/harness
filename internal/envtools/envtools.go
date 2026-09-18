// Package envtools names the modern CLI tools worth steering the agent
// towards when they are installed. Each is a better answer than the
// POSIX default a model reaches for by habit: rg over grep, fd over
// find, jq over grepping JSON, sd over sed. Naming the ones that are
// actually on PATH is the cheapest possible steering: a handful of
// words, resolved once, and never a claim about a tool that is not
// installed.
package envtools

import (
	"os/exec"
	"strings"
	"sync"
)

// Tool is one modern CLI tool and what it replaces or what it is for.
type Tool struct {
	Name string
	Note string
}

var modernTools = []Tool{
	{"rg", "grep"},
	{"fd", "find"},
	{"jq", "JSON"},
	{"yq", "YAML/TOML"},
	{"sd", "sed"},
	{"ast-grep", "structural search"},
	{"difft", "structural diff"},
	{"delta", "git diffs"},
	{"gh", "GitHub"},
	{"eza", "ls"},
	{"bat", "cat"},
	{"hyperfine", "benchmarking"},
	{"tokei", "code stats"},
}

// available resolves once: PATH does not change under a running
// session, and the summary is built per prompt and per tool
// description.
var available = sync.OnceValue(func() []Tool {
	return FilterOnPath(modernTools, exec.LookPath)
})

// Available returns the installed tools in list order.
func Available() []Tool { return available() }

// FilterOnPath keeps the tools lookPath resolves, preserving order.
// Separated from Available so tests can drive it without touching the
// process-wide memo.
func FilterOnPath(tools []Tool, lookPath func(string) (string, error)) []Tool {
	var found []Tool
	for _, t := range tools {
		if _, err := lookPath(t.Name); err == nil {
			found = append(found, t)
		}
	}
	return found
}

// Summary renders the installed tools as "name (note), ..." for the
// shell tool description and the system prompt's environment block, so
// both surfaces name exactly what is installed from one source. Empty
// when none are installed; callers then omit the steering entirely.
func Summary() string {
	return summary(Available())
}

func summary(tools []Tool) string {
	if len(tools) == 0 {
		return ""
	}
	parts := make([]string, len(tools))
	for i, t := range tools {
		parts[i] = t.Name + " (" + t.Note + ")"
	}
	return strings.Join(parts, ", ")
}
