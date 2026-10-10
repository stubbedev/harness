// Package procscope starts long-running child processes - terminal
// shells, language servers, MCP servers - in systemd scopes of their own,
// so the kernel killing one of them for running out of memory cannot take
// Harness down with it.
//
// Run inside a systemd unit (a tmux pane or terminal started by the user
// manager, an ssh login), a kernel OOM kill of any process in that unit
// makes systemd stop the whole unit under its default OOMPolicy=stop: the
// terminal, tmux and Harness included, though only the child was at
// fault. In a scope of its own the kill lands in the child's unit, and
// OOMPolicy=continue keeps even that unit going: only the process the
// kernel chose dies.
//
// Scopes are a Linux concept. Elsewhere, and on Linux without a systemd
// user manager, commands start unchanged and every Scope is nil; the
// methods of a nil Scope do nothing.
package procscope

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/stubbedev/harness/internal/envvars"
)

// OOMScoreAdj is the oom_score_adj a contained process is given, and
// every process it starts inherits. It sits above the 200 a systemd user
// manager gives its units, so when the whole machine runs out of memory
// the kernel picks a child Harness started before Harness or the terminal
// around it.
const OOMScoreAdj = 500

// The policies Harness contains its children with.
var (
	// Shell contains terminal sessions. A session's processes share a
	// cap of 75% of physical memory by default, which leaves the rest
	// for Harness, the terminal it runs in and the desktop, so a runaway
	// command is stopped well before the machine is.
	Shell = NewPolicy("shell", envvars.ShellMemoryMax, "75%", "Terminal sessions")
	// Servers contains language servers and stdio MCP servers, each in a
	// scope of its own with a cap of 50% of physical memory by default:
	// enough for a language server indexing a large tree, while a server
	// that leaks is stopped before it starves the agent's own commands.
	Servers = NewPolicy("server", envvars.LSPMCPMemoryMax, "50%", "Language and MCP servers")
)

// maxLabel bounds the part of a unit name a caller chooses. systemd
// allows 255 bytes in all; a server name is a label, not a path.
const maxLabel = 64

// unitSeq numbers the scopes this process creates, so two children
// started in the same instant still get distinct unit names.
var unitSeq atomic.Uint64

// Policy is how one kind of child is contained: the variable that sets
// its memory cap, the cap when that is unset, and the outcome of probing
// once whether systemd-run can create such scopes here.
type Policy struct {
	kind     string
	envVar   string
	fallback string
	what     string
	probe    func() containment
}

// containment is the outcome of a probe: the systemd-run binary and the
// properties each scope is created with, or nothing when children cannot
// be contained here.
type containment struct {
	systemdRun string
	props      []string
}

// NewPolicy returns a policy whose memory cap is read from envVar, in
// systemd's MemoryMax syntax, defaulting to fallback. kind names the
// policy in the probe's unit name; what names the children it contains
// in the warning logged when they cannot be. The probe runs on first use
// and its answer is kept for the life of the process: it spawns a
// process, and the answer does not change while Harness runs.
func NewPolicy(kind, envVar, fallback, what string) *Policy {
	p := &Policy{kind: kind, envVar: envVar, fallback: fallback, what: what}
	p.probe = sync.OnceValue(func() containment { return probeContainment(p) })
	return p
}

// limit is the memory cap this policy's scopes get: the environment's,
// or the fallback.
func (p *Policy) limit() string {
	if v := strings.TrimSpace(os.Getenv(p.envVar)); v != "" {
		return v
	}
	return p.fallback
}

// scopeProps returns the scope properties for a memory cap. MemoryMax
// turns a system-wide OOM into one confined to the scope, and
// MemorySwapMax=0 makes it come quickly: a runaway that may swap pushes
// the whole desktop into swap before it is stopped. "infinity" keeps the
// scope, and so the OOMPolicy, without the cap.
func scopeProps(limit string) []string {
	props := []string{"OOMPolicy=continue"}
	if !strings.EqualFold(limit, "infinity") {
		props = append(props, "MemoryMax="+limit, "MemorySwapMax=0")
	}
	return props
}

// Command returns the program and arguments that start command in a
// scope of its own, and that scope; command and args unchanged, and a nil
// scope, where children cannot be contained or command cannot be found.
//
// label names the scope's unit, harness-<label>-<pid>-<n>.scope; it is
// sanitised to the characters a unit name may hold. systemd-run --scope
// registers the scope and then execs the command in place, so the process
// the caller starts keeps its pid, its stdio, its environment and its
// working directory - they are the command's own.
//
// command is resolved through PATH here, by the caller's PATH, the way
// os/exec would have resolved it. systemd-run would otherwise search the
// child's environment, which a server's configuration may override. A
// command that is not found is returned unwrapped, so the caller reports
// the familiar error for it rather than one from systemd-run.
func (p *Policy) Command(label, command string, args []string) (string, []string, *Scope) {
	c := p.probe()
	if c.props == nil {
		return command, args, nil
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return command, args, nil
	}
	unit := UnitName(label, os.Getpid(), unitSeq.Add(1))
	return c.systemdRun, wrapArgs(unit, c.props, resolved, args), &Scope{unit: unit}
}

// UnitName returns the name of the n-th scope process pid creates for
// label.
func UnitName(label string, pid int, n uint64) string {
	return fmt.Sprintf("harness-%s-%d-%d.scope", SanitizeLabel(label), pid, n)
}

// SanitizeLabel maps label onto the characters a systemd unit name may
// hold without escaping: ASCII letters, digits, '_', '.' and '-'. Anything
// else becomes '_', and an empty label becomes "proc".
func SanitizeLabel(label string) string {
	var b strings.Builder
	for _, r := range label {
		if b.Len() >= maxLabel {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "proc"
	}
	return b.String()
}

// scopeArgs returns the systemd-run options that create unit as a
// transient scope of the user manager with props, up to but not
// including the command.
func scopeArgs(unit string, props []string) []string {
	args := []string{"--user", "--scope", "--quiet", "--collect", "--unit=" + unit}
	for _, p := range props {
		args = append(args, "--property="+p)
	}
	return args
}

// wrapArgs returns systemd-run's arguments for running command with args
// in unit.
func wrapArgs(unit string, props []string, command string, args []string) []string {
	argv := append(scopeArgs(unit, props), "--", command)
	return append(argv, args...)
}

// Scope is the systemd scope one contained child runs in. A nil Scope is
// a child that is not contained; every method then does nothing.
type Scope struct {
	unit string

	mu  sync.Mutex
	dir string

	reapOnce sync.Once
}

// Unit returns the scope's unit name, or "" for a nil scope.
func (s *Scope) Unit() string {
	if s == nil {
		return ""
	}
	return s.unit
}

// Dir returns the scope's cgroup directory, or "" while it is not known:
// systemd registers the scope just before the child is exec'd, and after
// the scope is gone there is nothing to find.
func (s *Scope) Dir() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		s.dir = FindCgroup(s.unit)
	}
	return s.dir
}

// Kill kills every process in the scope at once, the ones that left the
// child's process group and session included. The scope is collected
// once it is empty.
//
// A child killed while systemd-run is still registering its scope - in
// the first milliseconds, before the command is exec'd - can leave
// systemd holding the scope "running" with nothing in it, where
// --collect never comes for it. So once, a little after the first kill,
// a scope that is still there is stopped through the user manager.
func (s *Scope) Kill() {
	if dir := s.Dir(); dir != "" {
		KillCgroup(dir)
	}
	if s != nil {
		s.reapOnce.Do(func() { go reapLater(s.unit) })
	}
}

// OOMKills reports how many processes in the scope the kernel has killed
// for running it out of memory, and whether that is known at all.
func (s *Scope) OOMKills() (int, bool) {
	dir := s.Dir()
	if dir == "" {
		return 0, false
	}
	return CgroupOOMKills(dir)
}

// MemoryMax reports the scope's memory limit in bytes; false when it has
// none or is not known.
func (s *Scope) MemoryMax() (int64, bool) {
	dir := s.Dir()
	if dir == "" {
		return 0, false
	}
	return CgroupMemoryMax(dir)
}

// RaiseOOMScore raises the oom_score_adj of every process in the scope
// (see OOMScoreAdj), for a caller that never learns the child's pid.
func (s *Scope) RaiseOOMScore() {
	dir := s.Dir()
	if dir == "" {
		return
	}
	for _, pid := range CgroupProcs(dir) {
		RaiseOOMScore(pid)
	}
}
