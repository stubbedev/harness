package stringext

import (
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func Capitalize(text string) string {
	return cases.Title(language.English, cases.Compact).String(text)
}

// SplitFrontmatter extracts YAML frontmatter and body from markdown
// content. The frontmatter is everything between the first non-blank
// "---" line and its closing "---"; the body is what follows it. A
// UTF-8 BOM is stripped and line endings normalized to \n before
// parsing. Shared by skill and subagent file loading.
func SplitFrontmatter(content string) (frontmatter, body string, err error) {
	// Strip UTF-8 BOM for compatibility with editors that include it.
	content = strings.TrimPrefix(content, "\ufeff")
	// Normalize line endings to \n for consistent parsing.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	lines := strings.Split(content, "\n")
	start := slices.IndexFunc(lines, func(line string) bool {
		return strings.TrimSpace(line) != ""
	})
	if start == -1 || strings.TrimSpace(lines[start]) != "---" {
		return "", "", errors.New("no YAML frontmatter found")
	}

	endOffset := slices.IndexFunc(lines[start+1:], func(line string) bool {
		return strings.TrimSpace(line) == "---"
	})
	if endOffset == -1 {
		return "", "", errors.New("unclosed frontmatter")
	}
	end := start + 1 + endOffset

	frontmatter = strings.Join(lines[start+1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return frontmatter, body, nil
}

// NormalizeSpace normalizes whitespace in the given content string.
// It replaces Windows-style line endings with Unix-style line endings,
// converts tabs to four spaces, and trims leading and trailing newlines.
// Per-line indentation is preserved: trimming spaces would eat the first
// line's leading whitespace and corrupt indentation in code previews.
func NormalizeSpace(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\t", "    ")
	content = strings.Trim(content, "\n")
	return content
}

// IsValidBase64 reports whether s is canonical base64 under standard
// encoding (RFC 4648). It requires that s round-trips through
// decode/encode unchanged — rejecting whitespace, missing padding,
// and other leniencies that DecodeString alone would accept.
func IsValidBase64(s string) bool {
	if s == "" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	// Round-trip check rejects whitespace, missing padding, and other
	// leniencies that DecodeString silently accepts.
	return base64.StdEncoding.EncodeToString(decoded) == s
}

// xmlEscaper replaces the five characters that are not valid unescaped in
// XML text content or attribute values.
var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")

// EscapeXML escapes s for safe inclusion in generated XML, e.g. the
// <available_skills>/<available_subagents> prompt blocks.
func EscapeXML(s string) string {
	return xmlEscaper.Replace(s)
}

// Truncate shortens s to at most maxRunes runes, replacing the tail
// with marker when it has to cut. The marker counts toward maxRunes, so
// the result never exceeds it. Runes are never split, so the result is
// always valid UTF-8 when s is.
func Truncate(s string, maxRunes int, marker string) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	keep := max(maxRunes-utf8.RuneCountInString(marker), 0)
	i := 0
	for range keep {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i] + marker
}
