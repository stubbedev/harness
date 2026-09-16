//go:build !windows

package term

// Enter is the byte sequence that submits a line to the session's
// shell. A line feed is what the line discipline hands the shell when
// a person presses Enter (ICRNL), and every line editor accepts it.
const Enter = "\n"
