package common

import (
	"strings"
)

// StripShellDisplayPrefix peels leading "cd <path> && "/"cd <path>; "/
// "cd <path>\n" segments from a command for display. The model routinely
// prepends a cd to set the directory a command runs in; that is noise in
// the transcript, and the directory the command ran in still shows in the
// result's <cwd> tag, so stripping it loses nothing. A bare "cd <path>"
// with no separator is left alone: that is a navigation command, not a
// prefix to something else.
func StripShellDisplayPrefix(cmd string) string {
	original := cmd
	for {
		cmd = strings.TrimLeft(cmd, " \t\n")
		token, afterCd := firstToken(cmd)
		if token != "cd" {
			break
		}
		path, sep, rest, ok := splitCdBareword(afterCd)
		if !ok || sep == "" {
			break
		}
		_ = path
		cmd = rest
	}
	if strings.TrimSpace(cmd) == "" {
		return original
	}
	return cmd
}

// firstToken splits cmd at its first run of whitespace, returning the
// leading token and the remainder. Leading whitespace is consumed first.
func firstToken(cmd string) (token, rest string) {
	cmd = strings.TrimLeft(cmd, " \t\n")
	i := strings.IndexAny(cmd, " \t\n")
	if i < 0 {
		return cmd, ""
	}
	return cmd[:i], cmd[i:]
}

// splitCdBareword parses what follows the "cd" token: whitespace, a bare
// or quoted path, and the command separator that bounds it. ok is false
// when the structure is not a "cd <path><sep>" prefix.
func splitCdBareword(afterCd string) (path, sep, rest string, ok bool) {
	s := strings.TrimLeft(afterCd, " \t")
	if s == "" {
		return "", "", "", false
	}
	switch s[0] {
	case '"':
		i := strings.IndexByte(s[1:], '"')
		if i < 0 {
			return "", "", "", false
		}
		path = s[1 : 1+i]
		s = s[1+i+1:]
	case '\'':
		i := strings.IndexByte(s[1:], '\'')
		if i < 0 {
			return "", "", "", false
		}
		path = s[1 : 1+i]
		s = s[1+i+1:]
	default:
		// Bare path: run up to the first separator or whitespace-then-sep.
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == '&' || c == ';' || c == '\n' {
				path = strings.TrimRight(s[:i], " \t")
				s = s[i:]
				break
			}
			if c == ' ' || c == '\t' {
				// Whitespace after the path: the next non-space run must
				// be a separator, else this is not a cd prefix.
				path = s[:i]
				s = s[i:]
				break
			}
		}
		if path == "" {
			return "", "", "", false
		}
	}
	// Find the separator (&&, ;, \n) after any optional whitespace.
	s = strings.TrimLeft(s, " \t")
	switch {
	case strings.HasPrefix(s, "&&"):
		return path, "&&", s[len("&&"):], true
	case strings.HasPrefix(s, ";"):
		return path, ";", s[1:], true
	case strings.HasPrefix(s, "\n"):
		return path, "\n", s[1:], true
	}
	return "", "", "", false
}
