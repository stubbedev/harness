package tools

import (
	"os/exec"
	"strings"
	"sync"
)

// modernTools are the commands worth naming in the shell tool's
// description when they are installed. Each one is a better answer than
// the POSIX default a model reaches for by habit -- rg over grep, fd over
// find, jq over grepping JSON, sd over sed -- and naming the ones that
// are actually here is the cheapest possible way to steer towards them:
// a handful of words, checked once, and never a claim about something
// that is not installed.
//
// Keep the list short and keep it to tools that change how a task is
// done. Anything the model would reach for anyway does not earn a word.
var modernTools = []string{
	"rg",       // grep
	"fd",       // find
	"jq",       // JSON
	"yq",       // YAML/TOML
	"sd",       // sed
	"ast-grep", // structural search and rewrite
	"difft",    // structural diff
	"delta",    // readable git diffs
	"gh",       // GitHub
	"eza",      // ls
	"bat",      // cat
	"hyperfine",
	"tokei",
}

// availableModernTools returns the subset of modernTools on PATH, in the
// order listed. Resolved once: PATH does not change under a running
// session, and the shell tool's description is built per agent.
var availableModernTools = sync.OnceValue(func() string {
	var found []string
	for _, name := range modernTools {
		if _, err := exec.LookPath(name); err == nil {
			found = append(found, name)
		}
	}
	return strings.Join(found, ", ")
})
