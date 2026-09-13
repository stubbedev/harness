//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package term

// secretRead has no termios to sample on this platform; callers fall
// back to their text heuristics.
func (s *Session) secretRead() SecretReadState {
	return SecretReadUnknown
}
