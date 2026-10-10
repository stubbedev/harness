package term

import (
	"os"

	"github.com/stubbedev/harness/internal/procscope"
)

// Terminal sessions are fenced off from Harness by procscope: each shell
// runs in a systemd scope of its own, capped by procscope.Shell, where
// that can be done (see that package for why). These wrappers are the
// session's only contact with it; elsewhere they leave the shell as it
// is and report no scope.

// containedCommand returns the program and arguments that start shell in
// a scope of its own, and the unit's name; shell and args unchanged, and
// no unit, where sessions cannot be contained. systemd-run --scope
// registers the scope and then execs the shell in place, so the process
// Harness waits on, its pid and its terminal are the shell's own.
func containedCommand(shell string, args []string) (string, []string, string) {
	name, argv, scope := procscope.Shell.Command("shell", shell, args)
	return name, argv, scope.Unit()
}

// afterContainedStart raises the shell's oom_score_adj (see
// procscope.OOMScoreAdj), so in a machine-wide OOM the kernel picks a
// command the agent ran before Harness or the terminal around it.
func afterContainedStart(proc *os.Process) { procscope.RaiseOOMScore(proc.Pid) }

// scopeCgroup returns the cgroup directory of the session's scope, or ""
// while it is not known.
func scopeCgroup(pid int, unit string) string { return procscope.CgroupOf(pid, unit) }

// cgroupOOMKills reads how many processes the kernel has killed for
// running the cgroup out of memory.
func cgroupOOMKills(dir string) (int, bool) { return procscope.CgroupOOMKills(dir) }

// cgroupMemoryMax reads the cgroup's memory limit in bytes; false when
// it has none.
func cgroupMemoryMax(dir string) (int64, bool) { return procscope.CgroupMemoryMax(dir) }

// killCgroup kills every process in the cgroup at once, the ones that
// left the shell's process group and session included.
func killCgroup(dir string) { procscope.KillCgroup(dir) }
