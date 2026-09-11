package tools

import (
	"fmt"
	"strings"
)

// Named keys for the bash tool's terminal session. Raw text covers most
// interaction, but the keys that actually drive a program - escape,
// arrows, ctrl-c, function keys - are escape sequences that are easy to
// get subtly wrong when written by hand, so they get names instead.
// The sequences are the xterm defaults every program expects.

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

// SupportedKeys lists the key names for the tool description.
const SupportedKeys = "enter, tab, backtab, escape, space, backspace, delete, " +
	"up, down, left, right, home, end, pageup, pagedown, insert, f1-f12, " +
	"and any ctrl+<letter> (ctrl+c, ctrl+d, ctrl+z, ...)"

// keyBytes translates one key name to the bytes to write to the
// terminal. Unknown names are an error rather than a silent no-op: a
// dropped keystroke leaves the caller reading a screen that never
// changed, with nothing to explain why.
func keyBytes(name string) ([]byte, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if b, ok := namedKeys[n]; ok {
		return b, nil
	}
	// Generic ctrl+<letter>.
	if rest, ok := strings.CutPrefix(n, "ctrl+"); ok && len(rest) == 1 {
		if c := rest[0]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return []byte{strings.ToUpper(rest)[0] - 'A' + 1}, nil
		}
	}
	return nil, fmt.Errorf("unknown key %q; supported: %s", name, SupportedKeys)
}

// parseKeys turns a comma-separated key list ("escape, :, w, q, enter")
// into one byte sequence per key - kept separate so the caller can pace
// them. Single printable characters are taken literally so a key
// sequence can spell out the keystrokes it types.
func parseKeys(list string) ([][]byte, error) {
	var out [][]byte
	for part := range strings.SplitSeq(list, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if len([]rune(name)) == 1 && name != " " {
			out = append(out, []byte(name))
			continue
		}
		b, err := keyBytes(name)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no keys given; supported: %s", SupportedKeys)
	}
	return out, nil
}
