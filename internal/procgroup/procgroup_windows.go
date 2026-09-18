//go:build windows

package procgroup

import (
	"os"
	"time"
)

func killGroup(proc *os.Process, _ time.Duration) {
	// No process groups on Windows: kill the direct child. Console
	// children share the ConPTY/job object teardown once it closes.
	_ = proc.Kill()
}
