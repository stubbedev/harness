//go:build linux

package term

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stubbedev/harness/internal/envvars"
)

// defaultShellMemoryMax is the memory a terminal session's processes may
// hold between them, as systemd reads MemoryMax: a share of physical
// memory. It leaves the rest for Harness, the terminal it runs in and the
// desktop, so a runaway command is stopped well before the machine is.
const defaultShellMemoryMax = "75%"

// shellOOMScoreAdj is the oom_score_adj a session's shell is given, and
// every process it starts inherits. It sits above the 200 a systemd user
// manager gives its units, so when the whole machine runs out of memory
// the kernel picks a command the agent ran before Harness or the
// terminal around it.
const shellOOMScoreAdj = 500

// containProbeTimeout bounds the one-time check that systemd-run can
// create scopes here. A user manager that is not answering must not hold
// the first terminal session for long.
const containProbeTimeout = 3 * time.Second

// unitSeq numbers the scopes this process creates, so two sessions
// opened in the same instant still get distinct unit names.
var unitSeq atomic.Uint64

// containment is how terminal sessions are fenced off from Harness:
// the systemd-run invocation that puts a shell in a scope of its own,
// or nothing when that cannot be done here.
type containment struct {
	props []string
}

// shellContainment is decided once per process. The systemd-run probe
// spawns a process, and its answer does not change while Harness runs.
var shellContainment = sync.OnceValue(probeContainment)

// probeContainment works out whether shells can be put in scopes of
// their own, and with which properties.
//
// The scope is what keeps a command's out-of-memory kill from taking
// Harness with it. Run inside a systemd unit (a tmux pane or terminal
// started by the user manager, an ssh login), a kernel OOM kill of any
// process in that unit makes systemd stop the whole unit under its
// default OOMPolicy=stop - the terminal, tmux and Harness included,
// though only the command was at fault. In a scope of its own the kill
// lands in the shell's unit, and OOMPolicy=continue keeps even that
// shell alive: only the process the kernel chose dies.
//
// MemoryMax turns the system-wide OOM into one confined to the scope,
// and MemorySwapMax=0 makes it come quickly: a runaway that may swap
// pushes the whole desktop into swap before it is stopped.
func probeContainment() containment {
	limit := strings.TrimSpace(os.Getenv(envvars.ShellMemoryMax))
	if strings.EqualFold(limit, "off") {
		return containment{}
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return containment{}
	}
	if limit == "" {
		limit = defaultShellMemoryMax
	}
	props := []string{"OOMPolicy=continue"}
	if !strings.EqualFold(limit, "infinity") {
		props = append(props, "MemoryMax="+limit, "MemorySwapMax=0")
	}
	if err := runProbe(props); err != nil {
		slog.Warn("Terminal sessions run without a memory scope; systemd-run could not create one", "error", err)
		return containment{}
	}
	return containment{props: props}
}

func runProbe(props []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), containProbeTimeout)
	defer cancel()
	unit := fmt.Sprintf("harness-probe-%d.scope", os.Getpid())
	args := append(scopeArgs(unit, props), "--", "true")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "systemd-run", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func scopeArgs(unit string, props []string) []string {
	args := []string{"--user", "--scope", "--quiet", "--collect", "--unit=" + unit}
	for _, p := range props {
		args = append(args, "--property="+p)
	}
	return args
}

// containedCommand returns the program and arguments that start shell in
// a scope of its own, and the unit's name; shell and args unchanged, and
// no unit, where sessions cannot be contained. systemd-run --scope
// registers the scope and then execs the shell in place, so the process
// Harness waits on, its pid and its terminal are the shell's own.
func containedCommand(shell string, args []string) (string, []string, string) {
	c := shellContainment()
	if c.props == nil {
		return shell, args, ""
	}
	unit := fmt.Sprintf("harness-shell-%d-%d.scope", os.Getpid(), unitSeq.Add(1))
	argv := append(scopeArgs(unit, c.props), "--", shell)
	return "systemd-run", append(argv, args...), unit
}

// afterContainedStart raises the shell's oom_score_adj (see
// shellOOMScoreAdj). Raising it needs no privilege; a value already
// higher is left alone.
func afterContainedStart(proc *os.Process) {
	path := fmt.Sprintf("/proc/%d/oom_score_adj", proc.Pid)
	current, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if v, err := strconv.Atoi(strings.TrimSpace(string(current))); err == nil && v >= shellOOMScoreAdj {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(shellOOMScoreAdj)), 0o644)
}

// scopeCgroup returns the cgroup directory of the session's scope, or ""
// while it is not known. systemd-run moves itself into the scope before
// it execs the shell, so right after start the process can still be in
// Harness's own cgroup; the path is only trusted once it names the unit.
func scopeCgroup(pid int, unit string) string {
	if unit == "" {
		return ""
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		// The unified hierarchy is the "0::" line.
		rel, ok := strings.CutPrefix(line, "0::")
		if !ok || filepath.Base(rel) != unit {
			continue
		}
		return filepath.Join("/sys/fs/cgroup", rel)
	}
	return ""
}

// cgroupOOMKills reads how many processes the kernel has killed for
// running the cgroup out of memory.
func cgroupOOMKills(dir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "memory.events"))
	if err != nil {
		return 0, false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "oom_kill "); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			return n, err == nil
		}
	}
	return 0, false
}

// cgroupMemoryMax reads the cgroup's memory limit in bytes; false when
// it has none.
func cgroupMemoryMax(dir string) (int64, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "memory.max"))
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n, err == nil
}

// killCgroup kills every process in the cgroup at once, the ones that
// left the shell's process group and session included.
func killCgroup(dir string) {
	_ = os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0o644)
}
