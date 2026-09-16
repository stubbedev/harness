package tools

import "strings"

// Input to the shell tool is one string: the text a person would type,
// with the keys that have no character of their own - escape, arrows,
// ctrl-c, function keys - written by name in angle brackets, the way a
// person would say them: "<escape>:wq<enter>", "<ctrl+c>", "y<enter>".
// The sequences are the xterm defaults every program expects. A
// bracketed word that is not a key name is text and is typed as such,
// so "cat <file" or "echo <hello>" needs no escaping.

var namedKeys = map[string][]byte{
	"enter":     []byte("\r"),
	"return":    []byte("\r"),
	"cr":        []byte("\r"),
	"tab":       []byte("\t"),
	"backtab":   []byte("\x1b[Z"),
	"shift+tab": []byte("\x1b[Z"),
	"escape":    []byte("\x1b"),
	"esc":       []byte("\x1b"),
	"space":     []byte(" "),
	"backspace": []byte("\x7f"),
	"bs":        []byte("\x7f"),
	"delete":    []byte("\x1b[3~"),
	"del":       []byte("\x1b[3~"),
	"up":        []byte("\x1b[A"),
	"down":      []byte("\x1b[B"),
	"right":     []byte("\x1b[C"),
	"left":      []byte("\x1b[D"),
	"home":      []byte("\x1b[H"),
	"end":       []byte("\x1b[F"),
	"pageup":    []byte("\x1b[5~"),
	"pgup":      []byte("\x1b[5~"),
	"pagedown":  []byte("\x1b[6~"),
	"pgdn":      []byte("\x1b[6~"),
	"insert":    []byte("\x1b[2~"),
	"ins":       []byte("\x1b[2~"),
	"f1":        []byte("\x1bOP"),
	"f2":        []byte("\x1bOQ"),
	"f3":        []byte("\x1bOR"),
	"f4":        []byte("\x1bOS"),
	"f5":        []byte("\x1b[15~"),
	"f6":        []byte("\x1b[17~"),
	"f7":        []byte("\x1b[18~"),
	"f8":        []byte("\x1b[19~"),
	"f9":        []byte("\x1b[20~"),
	"f10":       []byte("\x1b[21~"),
	"f11":       []byte("\x1b[23~"),
	"f12":       []byte("\x1b[24~"),
}

// maxKeyNameLen bounds how far past a "<" the tokenizer looks for the
// closing ">": longer than the longest key name is text, not a key.
const maxKeyNameLen = 12

// keyBytes translates one key name to the bytes to write to the
// terminal, reporting whether the name is a key at all.
func keyBytes(name string) ([]byte, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if b, ok := namedKeys[n]; ok {
		return b, true
	}
	// Generic ctrl+<letter>.
	if rest, ok := strings.CutPrefix(n, "ctrl+"); ok && len(rest) == 1 {
		if c := rest[0]; c >= 'a' && c <= 'z' {
			return []byte{c - 'a' + 1}, true
		}
	}
	return nil, false
}

// inputSegment is one run of an input string: text to be written as it
// is, or a named key with the bytes it stands for.
type inputSegment struct {
	text string
	key  []byte
}

// parseInput splits input into runs of text and named keys. Text runs
// are kept whole so a multi-line block can still be delivered as one
// paste; keys come out one at a time so the caller can pace them.
func parseInput(input string) []inputSegment {
	var segs []inputSegment
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			segs = append(segs, inputSegment{text: text.String()})
			text.Reset()
		}
	}
	for i := 0; i < len(input); {
		if input[i] == '<' {
			if end := strings.IndexByte(input[i:], '>'); end > 1 && end <= maxKeyNameLen+1 {
				if b, ok := keyBytes(input[i+1 : i+end]); ok {
					flush()
					segs = append(segs, inputSegment{key: b})
					i += end + 1
					continue
				}
			}
		}
		text.WriteByte(input[i])
		i++
	}
	flush()
	return segs
}

// hasKeys reports whether any segment is a named key.
func hasKeys(segs []inputSegment) bool {
	for _, s := range segs {
		if s.key != nil {
			return true
		}
	}
	return false
}
