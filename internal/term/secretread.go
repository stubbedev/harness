package term

// SecretReadState is what the terminal's line discipline says about the
// input the foreground program is waiting for, sampled from the master
// side of the PTY (which sees the slave's termios).
type SecretReadState int

const (
	// SecretReadUnknown: the termios is not observable on this
	// platform, so nothing can be concluded.
	SecretReadUnknown SecretReadState = iota
	// SecretReadNo: the terminal is echoing (an ordinary question) or
	// out of canonical mode (a line editor or full-screen program
	// reading keystrokes raw).
	SecretReadNo
	// SecretReadYes: line input with echo disabled - the state every
	// hidden-line reader (sudo, su, ssh, getpass, `read -s`) reads in.
	SecretReadYes
)

// SecretRead samples the terminal's input discipline to tell a
// credential prompt from everything else that can wait on the terminal.
//
// A program reading a secret clears ECHO but keeps the line discipline
// canonical - the tty still buffers and edits the line, it just does not
// print it. A line editor (readline, zle) or any raw-mode program
// (editors, pagers, TUIs) clears ICANON as well and does its own echo in
// software, so an idle shell prompt never reads as a secret; and an
// ordinary question leaves echo on. The combination - ECHO cleared,
// ICANON set - is locale-independent and program-independent, and it
// re-arms on a wrong password (the reader clears echo again for the
// retry).
//
// SecretReadUnknown is returned where the termios cannot be read;
// callers fall back to text heuristics there.
func (s *Session) SecretRead() SecretReadState {
	return s.secretRead()
}
