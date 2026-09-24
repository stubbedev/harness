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
		rest, ok := splitCdBareword(afterCd)
		if !ok {
			break
		}
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
// or quoted path, and the command separator that bounds it. It returns
// the remainder after the separator and whether the structure matched a
// "cd <path><sep>" prefix.
func splitCdBareword(afterCd string) (rest string, ok bool) {
	s := strings.TrimLeft(afterCd, " \t")
	if s == "" {
		return "", false
	}
	switch s[0] {
	case '"':
		i := strings.IndexByte(s[1:], '"')
		if i < 0 {
			return "", false
		}
		s = s[1+i+1:]
	case '\'':
		i := strings.IndexByte(s[1:], '\'')
		if i < 0 {
			return "", false
		}
		s = s[1+i+1:]
	default:
		// Bare path: run up to the first separator or whitespace.
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == '&' || c == ';' || c == '\n' {
				s = s[i:]
				break
			}
			if c == ' ' || c == '\t' {
				s = s[i:]
				break
			}
		}
	}
	// Find the separator (&&, ;, \n) after any optional whitespace.
	s = strings.TrimLeft(s, " \t")
	switch {
	case strings.HasPrefix(s, "&&"):
		return s[len("&&"):], true
	case strings.HasPrefix(s, ";"):
		return s[1:], true
	case strings.HasPrefix(s, "\n"):
		return s[1:], true
	}
	return "", false
}
