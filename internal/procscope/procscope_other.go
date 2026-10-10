//go:build !linux

package procscope

// Scopes and cgroups are Linux concepts: elsewhere children start
// unchanged and have no out-of-memory accounting to read.

func probeContainment(*Policy) containment { return containment{} }

func reapLater(string) {}

// RaiseOOMScore does nothing outside Linux.
func RaiseOOMScore(int) {}

// CgroupOf returns "" outside Linux.
func CgroupOf(int, string) string { return "" }

// FindCgroup returns "" outside Linux.
func FindCgroup(string) string { return "" }

// CgroupOOMKills knows nothing outside Linux.
func CgroupOOMKills(string) (int, bool) { return 0, false }

// CgroupMemoryMax knows nothing outside Linux.
func CgroupMemoryMax(string) (int64, bool) { return 0, false }

// CgroupProcs returns nothing outside Linux.
func CgroupProcs(string) []int { return nil }

// KillCgroup does nothing outside Linux.
func KillCgroup(string) {}
