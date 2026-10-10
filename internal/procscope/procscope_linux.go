//go:build linux

package procscope

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// cgroupRoot is where the unified cgroup hierarchy is mounted.
const cgroupRoot = "/sys/fs/cgroup"

// probeTimeout bounds the one-time check that systemd-run can create
// scopes here. A user manager that is not answering must not hold the
// first child for long.
const probeTimeout = 3 * time.Second

// reapDelay is how long after a kill a scope that is still there is
// taken to be stuck rather than on its way out.
const reapDelay = time.Second

// reapTimeout bounds asking the user manager to stop a stuck scope.
const reapTimeout = 3 * time.Second

// findDepth bounds the walk for a scope systemd placed somewhere other
// than where it usually does, below the user manager's own cgroup.
const findDepth = 3

// probeContainment works out whether p's children can be put in scopes
// of their own, and with which properties. It creates one throwaway
// scope with exactly those properties, so a cap systemd rejects is found
// here rather than on every child's start.
func probeContainment(p *Policy) containment {
	limit := p.limit()
	if strings.EqualFold(limit, "off") {
		return containment{}
	}
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		return containment{}
	}
	props := scopeProps(limit)
	if err := runProbe(systemdRun, p.kind, props); err != nil {
		slog.Warn(p.what+" run without a memory scope; systemd-run could not create one", "error", err)
		return containment{}
	}
	return containment{systemdRun: systemdRun, props: props}
}

func runProbe(systemdRun, kind string, props []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	unit := fmt.Sprintf("harness-probe-%s-%d.scope", SanitizeLabel(kind), os.Getpid())
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, systemdRun, wrapArgs(unit, props, "true", nil)...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// reapLater stops unit through the user manager if it is still there a
// little after it was killed (see Scope.Kill). A scope that went away on
// its own, the usual outcome, costs one lookup.
func reapLater(unit string) {
	time.Sleep(reapDelay)
	dir := FindCgroup(unit)
	if dir == "" {
		return
	}
	KillCgroup(dir)
	stopUnit(unit)
}

// stopUnit asks the user manager to stop unit without waiting for it.
func stopUnit(unit string) {
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), reapTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, systemctl, "--user", "stop", "--no-block", unit).Run(); err != nil {
		slog.Debug("Failed to stop a killed scope", "unit", unit, "error", err)
	}
}

// RaiseOOMScore raises the process's oom_score_adj to OOMScoreAdj.
// Raising it needs no privilege; a value already higher is left alone.
func RaiseOOMScore(pid int) {
	path := fmt.Sprintf("/proc/%d/oom_score_adj", pid)
	current, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if v, err := strconv.Atoi(strings.TrimSpace(string(current))); err == nil && v >= OOMScoreAdj {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(OOMScoreAdj)), 0o644)
}

// CgroupOf returns the cgroup directory of the process's scope, or ""
// while it is not known. systemd-run moves itself into the scope before
// it execs the command, so right after start the process can still be
// in Harness's own cgroup; the path is only trusted once it names unit.
func CgroupOf(pid int, unit string) string {
	if unit == "" {
		return ""
	}
	rel, ok := cgroupPath(fmt.Sprintf("/proc/%d/cgroup", pid))
	if !ok || filepath.Base(rel) != unit {
		return ""
	}
	return filepath.Join(cgroupRoot, rel)
}

// cgroupPath reads a process's place in the unified hierarchy, the "0::"
// line of a /proc/<pid>/cgroup file.
func cgroupPath(file string) (string, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rel, ok := strings.CutPrefix(line, "0::"); ok {
			return rel, true
		}
	}
	return "", false
}

// FindCgroup returns the cgroup directory of unit, a scope of this
// user's systemd manager, or "" when there is none. It needs no pid: a
// language server is started by a library that never hands its process
// out. systemd puts a transient user scope in app.slice, or at the root
// of the manager's cgroup on versions before that slice existed; those
// are looked at first, then a shallow walk covers any other slice.
func FindCgroup(unit string) string {
	if unit == "" {
		return ""
	}
	for _, root := range managerCgroups() {
		for _, dir := range []string{filepath.Join(root, "app.slice", unit), filepath.Join(root, unit)} {
			if isDir(dir) {
				return dir
			}
		}
		if dir := walkFor(root, unit); dir != "" {
			return dir
		}
	}
	return ""
}

// managerCgroups returns the cgroup directories the user's systemd
// manager may own: the one Harness itself runs below, when it does, and
// the standard place for this uid.
func managerCgroups() []string {
	uid := os.Getuid()
	manager := fmt.Sprintf("user@%d.service", uid)
	var roots []string
	if rel, ok := cgroupPath("/proc/self/cgroup"); ok {
		if i := strings.Index(rel, "/"+manager); i >= 0 {
			roots = append(roots, filepath.Join(cgroupRoot, rel[:i+1+len(manager)]))
		}
	}
	standard := filepath.Join(cgroupRoot, "user.slice", fmt.Sprintf("user-%d.slice", uid), manager)
	if len(roots) == 0 || roots[0] != standard {
		roots = append(roots, standard)
	}
	return roots
}

// walkFor looks for a directory named unit up to findDepth levels below
// root.
func walkFor(root, unit string) string {
	var found string
	base := strings.Count(root, string(filepath.Separator))
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be read holds nothing to find.
			return fs.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == unit {
			found = path
			return fs.SkipAll
		}
		if strings.Count(path, string(filepath.Separator))-base >= findDepth {
			return fs.SkipDir
		}
		return nil
	})
	return found
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// CgroupOOMKills reads how many processes the kernel has killed for
// running the cgroup out of memory.
func CgroupOOMKills(dir string) (int, bool) {
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

// CgroupMemoryMax reads the cgroup's memory limit in bytes; false when it
// has none.
func CgroupMemoryMax(dir string) (int64, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "memory.max"))
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n, err == nil
}

// CgroupProcs lists the processes in the cgroup.
func CgroupProcs(dir string) []int {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil
	}
	var pids []int
	for field := range strings.FieldsSeq(string(data)) {
		if pid, err := strconv.Atoi(field); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// KillCgroup kills every process in the cgroup at once, the ones that
// left the child's process group and session included.
func KillCgroup(dir string) {
	_ = os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0o644)
}
