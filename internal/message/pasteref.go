package message

import (
	"fmt"
	"strconv"
	"strings"
)

// The paste reference kind names. The editor's inline paste tokens and
// the transcript's attachment tags are minted by the same FormatRef and
// recognized by the same ParseRef, so the two surfaces can never drift
// into different spellings of the same reference.
const (
	RefKindImage = "Image"
	RefKindFile  = "File"
	RefKindPaste = "Pasted text"
)

// PasteLinesThreshold and PasteColsThreshold define when pasted text is
// too big to live in the prompt: the editor turns such a paste into a
// reference token, and the transcript shows the reference's content
// truncated by the same measure.
const (
	PasteLinesThreshold = 10
	PasteColsThreshold  = 1000
)

// RefKind classifies an attachment into the kind its reference tag
// names.
func (a Attachment) RefKind() string {
	switch {
	case a.IsImage():
		return RefKindImage
	case a.IsText():
		return RefKindPaste
	default:
		return RefKindFile
	}
}

// FormatRef renders the numbered bracket reference for a paste or
// attachment: "[Image #1]", "[Pasted text #2 +24 lines]". A positive
// lines count adds the line suffix.
func FormatRef(kind string, n, lines int) string {
	ref := fmt.Sprintf("[%s #%d", kind, n)
	if lines > 0 {
		ref += fmt.Sprintf(" +%d lines", lines)
	}
	return ref + "]"
}

// ParseRef parses one bracket reference as FormatRef produces it. ok
// reports whether s is such a reference; anything a user typed that
// only looks like one parses too, which is the point: a token the user
// edited back into reference shape behaves like one again.
func ParseRef(s string) (kind string, n, lines int, ok bool) {
	if len(s) < 3 || s[0] != '[' || s[len(s)-1] != ']' {
		return "", 0, 0, false
	}
	inner := s[1 : len(s)-1]
	hash := strings.Index(inner, " #")
	if hash <= 0 {
		return "", 0, 0, false
	}
	kind = inner[:hash]
	rest := inner[hash+2:]
	numStr, lineStr := rest, ""
	if plus := strings.Index(rest, " +"); plus >= 0 {
		numStr = rest[:plus]
		lineStr = strings.TrimSuffix(strings.TrimPrefix(rest[plus+1:], "+ "), " lines")
	}
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return "", 0, 0, false
	}
	switch kind {
	case RefKindImage, RefKindFile:
		if lineStr != "" {
			return "", 0, 0, false
		}
	case RefKindPaste:
		if lineStr != "" {
			lines, err = strconv.Atoi(lineStr)
			if err != nil || lines <= 0 {
				return "", 0, 0, false
			}
		}
	default:
		return "", 0, 0, false
	}
	return kind, n, lines, true
}
