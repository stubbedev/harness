//go:build !linux && !darwin && !windows

package procgroup

// The tree walk and the tty-holder sweep are Linux/macOS features (they
// need /proc or sysctl+lsof); elsewhere the group kill is all the
// teardown there is.

func descendants(_ int) []int { return nil }

func killHolders(string) {}
