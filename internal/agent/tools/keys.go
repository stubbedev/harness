package tools

// Input to the shell tool is the literal byte stream a terminal would
// receive: command text, with the keys that have no character of their
// own sent as their terminal sequences - "\n" (or "\r") for enter,
// "\u0003" for ctrl-c, "\u001b[A" for up. Nothing is parsed out of the
// text, so a heredoc body, a commit message or a redirection reaches
// the terminal exactly as given.

// containsKeyBytes reports whether input carries bytes the terminal
// treats as keys rather than text: escape, delete, or a control
// character other than newline. Newline is the documented enter and
// stays text, so a multi-line command still travels as one paste; a
// carriage return is an explicit enter, typed as such. Keyed input is
// program-bound keystrokes: it is typed into whatever runs, and it is
// never wrapped in paste markers, which would deliver its escape
// sequences as literal characters.
func containsKeyBytes(input string) bool {
	for i := 0; i < len(input); i++ {
		b := input[i]
		if b == 0x1b || b == 0x7f || b < 0x20 && b != '\n' {
			return true
		}
	}
	return false
}

// keyChunks splits input into the runs to write between pacing gaps:
// every escape sequence (CSI, SS3) and every lone escape is a chunk of
// its own, so a gap lands between keystrokes and never inside one, and
// the text between them groups into whole runs. Plain text comes back
// as one chunk and is sent in a single write.
func keyChunks(input string) []string {
	var chunks []string
	start := 0
	for i := 0; i < len(input); {
		if input[i] != 0x1b {
			i++
			continue
		}
		// A key ends the run it sits in.
		if start < i {
			chunks = append(chunks, input[start:i])
		}
		if n := escapeSequenceLen(input[i:]); n > 0 {
			chunks = append(chunks, input[i:i+n])
			i += n
		} else {
			// A lone escape: the gap after it is what keeps the next
			// bytes from reading as its meta suffix.
			chunks = append(chunks, input[i:i+1])
			i++
		}
		start = i
	}
	if start < len(input) {
		chunks = append(chunks, input[start:])
	}
	return chunks
}

// escapeSequenceLen returns the length of the escape sequence at the
// front of b, or 0 when b does not begin one: CSI (\x1b[ through its
// final byte) and SS3 (\x1bO plus one byte), the two shapes a keypress
// sends.
func escapeSequenceLen(b string) int {
	if len(b) < 2 || b[0] != 0x1b {
		return 0
	}
	switch b[1] {
	case '[':
		for i := 2; i < len(b); i++ {
			if c := b[i]; c >= 0x40 && c <= 0x7e {
				return i + 1
			}
		}
	case 'O':
		if len(b) >= 3 {
			return 3
		}
	}
	return 0
}

// endsKeyed reports whether input already ends in a keystroke: a
// control byte, or an escape sequence as the last chunk. Such input is
// typed exactly as given; anything else at a prompt gets the implied
// enter.
func endsKeyed(input string) bool {
	chunks := keyChunks(input)
	if len(chunks) == 0 {
		return false
	}
	last := chunks[len(chunks)-1]
	if last[0] == 0x1b {
		return true
	}
	b := last[len(last)-1]
	return b < 0x20 || b == 0x7f
}
