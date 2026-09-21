//go:build unix && !linux

package term

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// SampleJob inspects the terminal's foreground job through ps: macOS and
// the BSDs have no procfs. The process table gives each member's state,
// CPU time and resident set, which is enough for the progress and
// idleness signals; it does not say what a sleeping process waits on,
// so WchanReadable stays false and a fully idle job is taken as waiting
// for input, as it is on a Linux kernel that hides wait points. Each
// call advances the baseline the deltas are measured against.
func (s *Session) SampleJob() JobActivity {
	var act JobActivity
	pgrp := s.foregroundPgrp()
	if pgrp <= 0 {
		s.mu.Lock()
		s.waitSampleValid = false
		s.mu.Unlock()
		return act
	}
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,pgid=,stat=,cputime=,rss=").Output()
	if err != nil {
		return act
	}

	var cpu uint64
	var rssKB int64
	sawMember, allAsleep := false, true
	want := strconv.Itoa(pgrp)
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[1] != want {
			continue
		}
		sawMember = true
		switch fields[2][0] {
		case 'R', 'D', 'U', 'T':
			// Running, stuck on disk, or stopped: the job is doing
			// something other than waiting.
			allAsleep = false
		}
		cpu += cpuTimeTicks(fields[3])
		if kb, err := strconv.ParseInt(fields[4], 10, 64); err == nil {
			rssKB += kb
		}
	}
	if !sawMember {
		return act
	}

	s.mu.Lock()
	prevCPU, prevRSS, hadPrev := s.waitSampleCPU, s.waitSampleRSS, s.waitSampleValid
	s.waitSampleCPU, s.waitSampleRSS, s.waitSampleValid = cpu, rssKB, true
	s.mu.Unlock()

	act.Observed, act.Asleep = true, allAsleep
	act.CPUDelta = cpu
	act.RSSKB = rssKB
	if hadPrev {
		act.CPUDelta = cpu - prevCPU
		act.RSSDeltaKB = rssKB - prevRSS
	}
	return act
}

// cpuTimeTicks converts ps's cputime column - [[dd-]hh:]mm:ss[.cc] - to
// hundredths of a second, the same order of magnitude as the clock ticks
// procfs reports, so one threshold serves both.
func cpuTimeTicks(cputime string) uint64 {
	var days uint64
	if d, rest, ok := strings.Cut(cputime, "-"); ok {
		days, _ = strconv.ParseUint(d, 10, 64)
		cputime = rest
	}
	parts := strings.Split(cputime, ":")
	var seconds float64
	for _, part := range parts {
		v, _ := strconv.ParseFloat(part, 64)
		seconds = seconds*60 + v
	}
	return uint64((float64(days)*86400 + seconds) * 100)
}
