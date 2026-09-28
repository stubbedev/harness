//go:build !linux && !darwin && !windows

package procgroup

// The tree walk and the tty-holder sweep are Linux/macOS features (they
// need /proc or sysctl+lsof); elsewhere the group kill is all the
// teardown there is.

func descendants(_ int) []int { return nil }

func killHolders(string) {}

// stray is never collected here: descendants finds none.
type stray struct{}

func pin(int) stray { return stray{} }

func (stray) kill() {}

func (stray) release() {}
