package chat

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// A tool call's input arrives as a stream of JSON fragments; the UI sees
// the call long before the object is complete. partialStringField pulls
// one string field out of such a fragment so the transcript can show the
// command or file path as the model types it, instead of a bare spinner.
// It is a tolerant scan, not a parse: it finds `"key"`, the colon, the
// opening quote, and returns what follows up to the closing quote or the
// end of what has arrived, with JSON escapes resolved. The field is
// reported as found only once its opening quote is there.
func partialStringField(input, key string) (string, bool) {
	_, rest, found := strings.Cut(input, `"`+key+`"`)
	if !found {
		return "", false
	}
	rest = strings.TrimLeft(rest, " \t\r\n")
	if !strings.HasPrefix(rest, ":") {
		return "", false
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	if !strings.HasPrefix(rest, `"`) {
		return "", false
	}
	rest = rest[1:]

	var sb strings.Builder
	for j := 0; j < len(rest); j++ {
		c := rest[j]
		switch c {
		case '"':
			return sb.String(), true
		case '\\':
			if j+1 >= len(rest) {
				// An escape cut in half by the stream: stop before it.
				return sb.String(), true
			}
			j++
			switch rest[j] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case 'u':
				// A \uXXXX escape: decode it when complete, stop when the
				// stream cut it.
				if j+4 >= len(rest) {
					return sb.String(), true
				}
				if r, err := strconv.ParseUint(rest[j+1:j+5], 16, 32); err == nil && r <= 0x7fffffff {
					sb.WriteRune(rune(r))
				}
				j += 4
			default:
				sb.WriteByte(rest[j])
			}
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String(), true
}

// pendingDetail is the one-line form of a streaming input value:
// whitespace collapsed to one space-joined line, cut to fit.
func pendingDetail(value string, width int) string {
	line := FirstLine(value)
	if width <= 0 {
		return line
	}
	return ansi.Truncate(line, width, "…")
}
