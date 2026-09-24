//go:build linux

package term

import (
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ttyReadWchans are the kernel wait points that are a terminal read and
// nothing else.
var ttyReadWchans = []string{"tty_read", "n_tty_read"}

// ambiguousWaitWchans are wait points a terminal reader can sit in but
// that sockets, pipes and event loops sit in just as often: the generic
// interruptible sleep, and the multiplexers. A process parked in one is
// waiting for the agent only when the file it waits on is this
// terminal, which ttyReadBlocked checks through the syscall it is in.
// nanosleep and child-reaping are deliberately absent: sleeping on a
// timer is not waiting for the agent.
var ambiguousWaitWchans = []string{
	"wait_woken", "do_select", "do_poll", "ep_poll", "poll_schedule_timeout",
}

// SampleJob inspects the terminal's foreground job - the shell itself
// counts, because a builtin (read, a heredoc) blocks in the shell's own
// process while it stays in the foreground. Each call advances the
// baseline the deltas are measured against; ResetWaitSample starts a
// fresh measurement window.
func (s *Session) SampleJob() JobActivity {
	var act JobActivity
	pgrp := s.foregroundPgrp()
	if pgrp <= 0 {
		s.mu.Lock()
		s.waitSampleValid = false
		s.mu.Unlock()
		return act
	}

	var cpu uint64
	var rssKB int64
	sawMember, allAsleep, inputWait, wchanSeen := false, true, false, false
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return act
	}
	for _, entry := range entries {
		if !isDigits(entry.Name()) {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		// The comm field can contain spaces; everything after the last
		// ")" is fixed-position: state, ppid, pgrp, ..., utime, stime.
		txt := string(stat)
		rest, ok := strings.CutPrefix(txt[strings.LastIndex(txt, ")")+1:], " ")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 13 {
			continue
		}
		if pg, err := strconv.Atoi(fields[2]); err != nil || pg != pgrp {
			continue
		}
		sawMember = true
		switch fields[0] {
		case "R", "D", "T":
			// Running, stuck on disk, or stopped: the job is doing
			// something other than waiting.
			allAsleep = false
		}
		utime, _ := strconv.ParseUint(fields[11], 10, 64)
		stime, _ := strconv.ParseUint(fields[12], 10, 64)
		cpu += utime + stime
		rssKB += residentKB(entry.Name())

		wchan, err := os.ReadFile("/proc/" + entry.Name() + "/wchan")
		if err != nil {
			continue
		}
		w := strings.TrimSpace(string(wchan))
		if w == "" || w == "0" {
			continue
		}
		wchanSeen = true
		if slices.Contains(ttyReadWchans, w) || (slices.Contains(ambiguousWaitWchans, w) && ttyReadBlocked(entry.Name(), w, s.pty.Name())) {
			inputWait = true
		}
	}

	if !sawMember {
		return act
	}

	s.mu.Lock()
	prevCPU, prevRSS, hadPrev := s.waitSampleCPU, s.waitSampleRSS, s.waitSampleValid
	s.waitSampleCPU, s.waitSampleRSS, s.waitSampleValid = cpu, rssKB, true
	s.mu.Unlock()

	act.Observed, act.Asleep, act.InputWait, act.WchanReadable = true, allAsleep, inputWait, wchanSeen
	act.CPUDelta = cpu
	act.RSSKB = rssKB
	if hadPrev {
		act.CPUDelta = cpu - prevCPU
		act.RSSDeltaKB = rssKB - prevRSS
	}
	return act
}

// residentKB reads a process's resident set size from /proc/<pid>/statm.
func residentKB(pid string) int64 {
	statm, err := os.ReadFile("/proc/" + pid + "/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(statm))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize()) / 1024
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// ttyReadBlocked reports whether a process parked in an ambiguous wait
// point is waiting on this terminal. /proc/<pid>/syscall names the call
// it is blocked in and its arguments: a read whose descriptor is the
// terminal's slave is a terminal read; an epoll wait is one when the
// epoll instance watches a descriptor that is. A poll or select cannot
// be checked without reading the process's memory, so it is not taken
// as a terminal read: a dev server or test runner idling in one is the
// common case, and typing at it would go nowhere. When the syscall file
// is unreadable the same conservative answer applies.
func ttyReadBlocked(pid, wchan, slave string) bool {
	if slave == "" {
		return false
	}
	raw, err := os.ReadFile("/proc/" + pid + "/syscall")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return false
	}
	nr, err := strconv.Atoi(fields[0])
	if err != nil {
		return false
	}
	arg := func(i int) (int64, bool) {
		if len(fields) <= i {
			return 0, false
		}
		v, err := strconv.ParseInt(strings.TrimPrefix(fields[i], "0x"), 16, 64)
		return v, err == nil
	}
	switch {
	case nr == unix.SYS_READ || nr == unix.SYS_READV || nr == unix.SYS_PREAD64 ||
		nr == unix.SYS_PREADV || nr == unix.SYS_PREADV2:
		fd, ok := arg(1)
		return ok && fdIsTerminal(pid, fd, slave)
	case nr == unix.SYS_SPLICE || nr == unix.SYS_COPY_FILE_RANGE:
		// The zero-copy reads: current coreutils cat splices its input
		// rather than reading it, and blocks there on a terminal just as
		// a read would. The source descriptor is the first argument.
		fd, ok := arg(1)
		return ok && fdIsTerminal(pid, fd, slave)
	case nr == unix.SYS_SENDFILE:
		// sendfile(out, in, ...): the source is the second argument.
		fd, ok := arg(2)
		return ok && fdIsTerminal(pid, fd, slave)
	case wchan == "ep_poll":
		fd, ok := arg(1)
		return ok && epollWatchesTerminal(pid, fd, slave)
	}
	return false
}

// fdIsTerminal reports whether descriptor fd of process pid is the
// terminal's slave.
func fdIsTerminal(pid string, fd int64, slave string) bool {
	target, err := os.Readlink("/proc/" + pid + "/fd/" + strconv.FormatInt(fd, 10))
	return err == nil && target == slave
}

// epollWatchesTerminal reports whether the epoll instance behind epfd
// has the terminal's slave among the descriptors it watches, read from
// the "tfd:" lines of its fdinfo.
func epollWatchesTerminal(pid string, epfd int64, slave string) bool {
	info, err := os.ReadFile("/proc/" + pid + "/fdinfo/" + strconv.FormatInt(epfd, 10))
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(info), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "tfd:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		fd, err := strconv.ParseInt(fields[0], 10, 64)
		if err == nil && fdIsTerminal(pid, fd, slave) {
			return true
		}
	}
	return false
}
