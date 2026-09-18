// Package procgroup kills a process's whole process tree, which plain
// (*os.Process).Kill does not: it signals only the direct child, so a
// background or disowned grandchild that stayed in the group survives,
// and one holding a terminal's slave end keeps the session looking
// alive. Both the mvdan exec handler (cancellation) and the terminal
// session teardown (Close) route through it so neither can be survived.
//
// Coverage per platform:
//
//   - Linux: process-group kill plus a PPid walk over /proc (a child
//     that called setsid leaves the group but stays parented), plus a
//     sweep of every process still holding the session's tty device
//     (which also reaches double-forked daemons that kept stdio).
//   - macOS: the same two nets, the tree via sysctl kern.proc.all and
//     the holders via lsof.
//   - Windows: no signal groups; children are assigned to a job object
//     with kill-on-close, so closing or terminating the job takes the
//     whole tree. Assign at spawn: a process that spawns before the
//     assignment is not in the job.
//
// What none of this catches: a program that deliberately orphans
// itself (setsid plus closing stdio, or a double-fork whose parent
// chain died before teardown) holds nothing of ours and parents to
// init - only a cgroup or job scoped from spawn could reach it.
package procgroup

import (
	"os"
	"time"
)

// Kill kills proc's process tree. On Unix the group is signalled by
// negative PID; grace > 0 interrupts first (SIGINT, so well-behaved
// children run their traps) and escalates to SIGKILL once the grace
// window passes or the group dies first; grace <= 0 SIGKILLs outright,
// which is the right call for teardown. Either way stragglers that
// forked between the kill and the reap are swept by a second SIGKILL.
// On Windows the child's job object (see NewJob) is terminated.
func Kill(proc *os.Process, grace time.Duration) { killGroup(proc, grace) }

// KillHolders SIGKILLs every process still holding the terminal device
// open (Linux /proc fd walk, macOS lsof; a no-op elsewhere). It runs at
// session teardown on the session's own slave device, so a holder is
// by definition attached to a terminal that is going away.
func KillHolders(device string) { killHolders(device) }
