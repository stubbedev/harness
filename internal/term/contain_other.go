//go:build !linux

package term

import "os"

// Scopes and cgroups are Linux concepts: elsewhere sessions start the
// shell directly and have no out-of-memory accounting to read.

func containedCommand(shell string, args []string) (string, []string, string) {
	return shell, args, ""
}

func afterContainedStart(*os.Process) {}

func scopeCgroup(int, string) string { return "" }

func cgroupOOMKills(string) (int, bool) { return 0, false }

func cgroupMemoryMax(string) (int64, bool) { return 0, false }

func killCgroup(string) {}
