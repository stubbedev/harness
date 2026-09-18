// Package procgroup kills a process's whole process group, which plain
// (*os.Process).Kill does not: it signals only the direct child, so a
// background or disowned grandchild that stayed in the group survives,
// and one holding a terminal's slave end keeps the session looking
// alive. Both the mvdan exec handler (cancellation) and the terminal
// session teardown (Close) route through it so neither can be survived.
package procgroup

import (
	"os"
	"time"
)

// Kill kills proc's process group. On Unix the group is signalled by
// negative PID; grace > 0 interrupts first (SIGINT, so well-behaved
// children run their traps) and escalates to SIGKILL once the grace
// window passes or the group dies first; grace <= 0 SIGKILLs outright,
// which is the right call for teardown. Either way a straggler that
// forked between the kill and the reap is swept by a second SIGKILL.
//
// On Windows there is no process group: the direct child is killed and
// the platform's job-object/ConPTY teardown carries the rest.
func Kill(proc *os.Process, grace time.Duration) { killGroup(proc, grace) }
