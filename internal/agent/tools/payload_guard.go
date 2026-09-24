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

	// maxWriteBytes caps one write call. Past this, a payload is either a
	// generation runaway or bulk data the shell tool should generate.
	maxWriteBytes = 2 << 20
)

// repetitionLimits gates the degenerate repetition detector. The run
// fields catch the same non-empty line repeated consecutively; the unit
// fields catch one long line that is a single short unit repeated
// throughout.
type repetitionLimits struct {
	runLines int
	runBytes int
	lineMin  int
	unitMax  int
	unitReps int
}

var (
	// editRepetitionLimits is tuned for section replacements, where any
	// substantial repetition is a generation runaway.
	editRepetitionLimits = repetitionLimits{
		runLines: 16, runBytes: 4 << 10, lineMin: 2 << 10, unitMax: 64, unitReps: 32,
	}
	// writeRepetitionLimits is looser: write is the whole-file tool, and
	// data files legitimately contain repeated lines, so only bulk
	// runaways far past anything a model authors by hand trip it.
	writeRepetitionLimits = repetitionLimits{
		runLines: 64, runBytes: 64 << 10, lineMin: 8 << 10, unitMax: 64, unitReps: 128,
	}
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

// validateWritePayload guards the write tool against corrupted payloads: an
// absolute size cap past anything a model authors in one call, and the
// degenerate repetition detector at the looser whole-file thresholds.
func validateWritePayload(content string) error {
	if len(content) > maxWriteBytes {
		return fmt.Errorf(
			"content is %d bytes, over the %d-byte cap; write the file in sections, or generate bulk data with the shell tool",
			len(content), maxWriteBytes,
		)
	}
	if _, count := degenerateRepetition(content, writeRepetitionLimits); count > 0 {
		return fmt.Errorf(
			"content contains the same text repeated %d times, which looks like a corrupted payload; generate bulk repeated data with the shell tool instead",
			count,
		)
	}
	return nil
}

// degenerateRepetition reports degenerate repetition in s under the given
// limits: the same non-empty line repeated consecutively at least
// limits.runLines times totaling limits.runBytes, or a single long line that
// is one short unit repeated limits.unitReps times. Both shapes mark
// generation runaways; legitimate edits stay far below either threshold. It
// returns the repeated unit and its count, or an empty unit when s is clean.
func degenerateRepetition(s string, limits repetitionLimits) (string, int) {
	lines := strings.Split(s, "\n")
	runLine, runStart := "", 0
	for i, line := range lines {
		if line == runLine && line != "" {
			count := i - runStart + 1
			if count >= limits.runLines && count*len(line) >= limits.runBytes {
				return line, count
			}
			continue
		}
		runLine, runStart = line, i
	}
	for _, line := range lines {
		if len(line) < limits.lineMin {
			continue
		}
		if unit, reps := repeatedUnit(line, limits); reps >= limits.unitReps {
			return unit, reps
		}
	}
	return "", 0
}

// repeatedUnit reports whether line is a single unit of at most
// limits.unitMax bytes repeated throughout, returning that unit and its
// count.
func repeatedUnit(line string, limits repetitionLimits) (string, int) {
	n := len(line)
	for unit := 1; unit <= limits.unitMax && unit*limits.unitReps <= n; unit++ {
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
// because that path re-indents new_string to the file's style. The
// normalized result comes from norm, which a whitespace-tolerant
// replacement has already filled with it.
func verifyReplacement(norm *normCache, result string, edit EditOperation, corrected bool) error {
	if edit.OldString == "" {
		return nil
	}
	oldText, newText, resultText := edit.OldString, edit.NewString, result
	if corrected {
		oldText, newText, resultText = normalizeText(oldText), normalizeText(newText), norm.of(result).norm
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
