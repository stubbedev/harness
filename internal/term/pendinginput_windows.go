//go:build windows

package term

// PendingInput cannot observe the console input queue over ConPTY;
// callers treat the answer as "nothing pending" and fall back.
func (s *Session) PendingInput() int { return 0 }

// DiscardPendingInput cannot flush the console input queue over ConPTY.
func (s *Session) DiscardPendingInput() {}
