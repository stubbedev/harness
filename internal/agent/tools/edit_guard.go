package tools

import (
	"fmt"
	"strings"
)

const (
	// maxEditNewStringBytes caps one edit's new_string. Whole-file rewrites
	// belong to the write tool; an edit payload past this size is a
	// generation runaway, not an edit.
	maxEditNewStringBytes = 1 << 20
	// maxEditNewStringFileRatio bounds new_string against the size of the
	// file being edited, and maxEditNewStringFileFloor keeps that relative
	// cap from shrinking below a substantial replacement for small files.
	maxEditNewStringFileRatio = 4
	maxEditNewStringFileFloor = 64 << 10

	// repeatedRunLines and repeatedRunBytes gate the consecutive-line
	// repeat detector: the same non-empty line repeated often enough to be
	// a generation runaway, with a byte floor so short legitimate repeats
	// (separators, blank lines) never trip it.
	repeatedRunLines = 16
	repeatedRunBytes = 4 << 10
	// repeatedLineMin is the line length above which a single line is
	// checked for short-unit repetition; repeatedUnitMax bounds the unit
	// and repeatedUnitReps the repeat count.
	repeatedLineMin  = 2 << 10
	repeatedUnitMax  = 64
	repeatedUnitReps = 32
)

// validateEditSizes rejects new_string payloads wildly larger than the file
// being edited. A replacement several times the whole file is a corrupted
// payload, not a section edit.
func validateEditSizes(oldContent string, edits []EditOperation) error {
	limit := max(maxEditNewStringFileFloor, maxEditNewStringFileRatio*len(oldContent))
	for i, edit := range edits {
		if len(edit.NewString) > limit {
			return fmt.Errorf(
				"edit %d: new_string is %d bytes but the file is only %d bytes, which looks like a corrupted payload; re-read the section and resend the edit",
				i+1, len(edit.NewString), len(oldContent),
			)
		}
	}
	return nil
}

// degenerateRepetition reports degenerate repetition in s: the same
// non-empty line repeated consecutively at least repeatedRunLines times
// totaling repeatedRunBytes, or a single long line that is one short unit
// repeated repeatedUnitReps times. Both shapes mark generation runaways;
// legitimate edits stay far below either threshold. It returns the repeated
// unit and its count, or an empty unit when s is clean.
func degenerateRepetition(s string) (string, int) {
	lines := strings.Split(s, "\n")
	runLine, runStart := "", 0
	for i, line := range lines {
		if line == runLine && line != "" {
			count := i - runStart + 1
			if count >= repeatedRunLines && count*len(line) >= repeatedRunBytes {
				return line, count
			}
			continue
		}
		runLine, runStart = line, i
	}
	for _, line := range lines {
		if len(line) < repeatedLineMin {
			continue
		}
		if unit, reps := repeatedUnit(line); reps >= repeatedUnitReps {
			return unit, reps
		}
	}
	return "", 0
}

// repeatedUnit reports whether line is a single unit of at most
// repeatedUnitMax bytes repeated throughout, returning that unit and its
// count.
func repeatedUnit(line string) (string, int) {
	n := len(line)
	for unit := 1; unit <= repeatedUnitMax && unit*repeatedUnitReps <= n; unit++ {
		if n%unit != 0 {
			continue
		}
		repeated := true
		for i := unit; i < n; i++ {
			if line[i] != line[i%unit] {
				repeated = false
				break
			}
		}
		if repeated {
			return line[:unit], n / unit
		}
	}
	return "", 0
}

// verifyReplacement checks the invariant every applied edit must hold: the
// old text is gone and the new text is in. Exact matches are verified on
// the raw strings; whitespace-corrected matches on normalized text,
// because that path re-indents new_string to the file's style.
func verifyReplacement(result string, edit EditOperation, corrected bool) error {
	if edit.OldString == "" {
		return nil
	}
	oldText, newText, resultText := edit.OldString, edit.NewString, result
	if corrected {
		oldText, newText, resultText = normalizeText(oldText), normalizeText(newText), normalizeText(result)
	}
	if !strings.Contains(newText, oldText) && strings.Contains(resultText, oldText) {
		return fmt.Errorf("old_string is still present after replacement")
	}
	if newText != "" && !strings.Contains(resultText, newText) {
		return fmt.Errorf("new_string is missing after replacement")
	}
	return nil
}

// normalizeText whitespace-normalizes each line of s and joins them back
// with newlines, mirroring how whitespace-corrected matches compare text.
func normalizeText(s string) string {
	return joinNormalized(strings.Split(s, "\n"))
}
