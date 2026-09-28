package lsp

import (
	"unicode/utf8"

	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
)

// CharacterOffset converts a byte offset into line to the character
// offset a server negotiated in enc: bytes for UTF-8, UTF-16 code units
// (the LSP default) or code points for UTF-32. Search tools report byte
// columns; sending one as-is to a UTF-16 server lands after the symbol on
// any line with multi-byte text before it. An offset past the line is
// clamped to its end.
func CharacterOffset(line string, byteOffset int, enc powernap.OffsetEncoding) int {
	byteOffset = min(max(byteOffset, 0), len(line))
	prefix := line[:byteOffset]
	switch enc {
	case powernap.UTF8:
		return byteOffset
	case powernap.UTF16:
		units := 0
		for _, r := range prefix {
			if r >= 0x10000 {
				units += 2
			} else {
				units++
			}
		}
		return units
	default:
		return utf8.RuneCountInString(prefix)
	}
}
