//go:build unix

package term

import (
	"context"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ForegroundIsShell reports whether the terminal's foreground process
// group is the shell's own process group - the shape of a builtin that
// is blocking on input (read, select, a heredoc) rather than a command
// that runs. A plain builtin read keeps the terminal echoing, so no tty
// state tells it from a silent command; the process table does.
func (s *Session) ForegroundIsShell() bool {
	if s.proc == nil {
		return false
	}
	var pgrp int
	err := s.masterControl(func(fd int) {
		p, errno := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		if errno == nil {
			pgrp = p
		}
	})
	if err != nil || pgrp <= 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,pgid=").Output()
	if err != nil {
		return false
	}
	shell := strconv.Itoa(s.proc.Pid)
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[0] == shell && fields[1] == strconv.Itoa(pgrp) {
			return true
		}
	}
	return false
}
