//go:build !linux

package term

// SampleJob is only implementable where the OS exposes process state
// (procfs on Linux). Elsewhere it reports nothing observable, and
// callers fall back to their wait budgets.
func (s *Session) SampleJob() JobActivity {
	return JobActivity{}
}
