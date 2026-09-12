//go:build linux

package term

import (
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// inputWaitWchans are kernel wait points a process blocked on input -
// terminal reads, multiplexed waits - can sit in. "wait_woken" is the
// generic interruptible sleep on kernels that do not expose the
// specific function; nanosleep and child-reaping are deliberately
// absent: sleeping on a timer is not waiting for the agent.
var inputWaitWchans = []string{
	"tty_read", "n_tty_read", "wait_woken",
	"do_select", "do_poll", "ep_poll", "poll_schedule_timeout",
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
		if slices.Contains(inputWaitWchans, w) {
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

// foregroundPgrp returns the process group currently reading the
// terminal, or 0 when it cannot be determined.
func (s *Session) foregroundPgrp() int {
	pgrp, err := unix.IoctlGetInt(int(s.ptmx.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return 0
	}
	return pgrp
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}
