//go:build windows

package term

// SampleJob is only implementable where the OS exposes process state.
// ConPTY exposes nothing observable, and callers fall back to their
// wait budgets.
func (s *Session) SampleJob() JobActivity {
	return JobActivity{}
}

// KillForeground has no process-group model to act on under ConPTY.
func (s *Session) KillForeground() error {
	return nil
}
