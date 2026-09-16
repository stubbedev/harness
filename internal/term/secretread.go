package term

// SecretReadState is what the terminal's line discipline says about the
// input the foreground program is waiting for, sampled from the master
// side of the PTY (which sees the slave's termios).
type SecretReadState int

const (
	// SecretReadUnknown: the termios is not observable on this
	// platform (Windows, where ConPTY has none), so nothing can be
	// concluded from the terminal itself.
	SecretReadUnknown SecretReadState = iota
	// SecretReadNo: the terminal is echoing, so whatever is typed is
	// shown - an ordinary question, or an idle prompt without a line
	// editor.
	SecretReadNo
	// SecretReadYes: line input with echo disabled - the shape getpass,
	// su, ssh, `read -s` and most hidden-line readers leave the terminal
	// in. Nothing else reads this way, so it alone is proof of a
	// credential prompt.
	SecretReadYes
	// SecretReadRaw: echo disabled and line editing off. A credential
	// reader that takes the line itself sits here (sudo since 1.9 reads
	// in cbreak mode so it can react to ctrl-c and show feedback), but so
	// do line editors, REPLs and full-screen programs, which echo in
	// software. On its own it proves nothing; together with a
	// prompt-shaped line of output it is a credential prompt.
	SecretReadRaw
)

// SecretRead samples the terminal's input discipline to tell a
// credential prompt from everything else that can wait on the terminal.
//
// The one bit every hidden-line reader agrees on is ECHO cleared. What
// they do with ICANON differs: getpass-style readers keep the tty
// buffering the line (SecretReadYes), sudo takes the keystrokes itself
// (SecretReadRaw). Line editors (readline, zle) and raw-mode programs
// clear both as well, so the raw state needs the prompt text to
// disambiguate it; an ordinary question leaves echo on and is never
// mistaken for either. The state is locale-independent and re-arms on a
// wrong password: the reader clears echo again for the retry.
//
// SecretReadUnknown is returned where the termios cannot be read;
// callers fall back to text heuristics there.
func (s *Session) SecretRead() SecretReadState {
	return s.secretRead()
}
