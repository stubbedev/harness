package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDegenerateRepetition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   string
		wantUnit  string
		wantCount int
	}{
		{
			name:      "repeated line runaway",
			payload:   strings.Repeat("\t\t\t\t\tsus service content\n", 400),
			wantUnit:  "\t\t\t\t\tsus service content",
			wantCount: 171, // 171x24 bytes is the first count past the 4KB gate.
		},
		{
			name:      "single long line of one short unit",
			payload:   strings.Repeat("sus ", 1000),
			wantUnit:  "sus ",
			wantCount: 1000,
		},
		{
			name:      "long enough run of substantial lines",
			payload:   strings.Repeat("line of text here\n", 300),
			wantUnit:  "line of text here",
			wantCount: 241, // 241x17 bytes is the first count past the 4KB gate.
		},
		{
			name:    "short repeat stays legitimate",
			payload: strings.Repeat("x\n", 15),
		},
		{
			name:    "short lines stay legitimate",
			payload: strings.Repeat("a normal line of code\n", 100),
		},
		{
			name:    "blank line runs stay legitimate",
			payload: strings.Repeat("\n", 1000),
		},
		{
			name:    "varied long lines stay legitimate",
			payload: variedLine(4096),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			unit, count := degenerateRepetition(tt.payload, editRepetitionLimits)
			require.Equal(t, tt.wantUnit, unit)
			require.Equal(t, tt.wantCount, count)
		})
	}
}

// variedLine builds a long line whose content changes throughout, so no
// short unit repeats across it.
func variedLine(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, " segment-%d", i)
	}
	return b.String()
}

func TestValidateEditsRejectsRunawayPayloads(t *testing.T) {
	t.Parallel()

	err := validateEdits([]EditOperation{{OldString: "a", NewString: strings.Repeat("sus service content\n", 400)}})
	require.ErrorContains(t, err, "corrupted payload")

	err = validateEdits([]EditOperation{{OldString: "a", NewString: strings.Repeat("x", maxEditNewStringBytes+1)}})
	require.ErrorContains(t, err, "write tool")

	require.NoError(t, validateEdits([]EditOperation{{OldString: "a", NewString: "b"}}))
}

func TestValidateWritePayload(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateWritePayload("package main\n\nfunc main() {}\n"))

	// Section-sized repetition stays under write's looser floor: the same
	// payload trips the edit detector.
	require.NoError(t, validateWritePayload(strings.Repeat("\t\t\t\t\tsus service content\n", 400)))

	// A modest file of repeated identical rows stays legitimate.
	require.NoError(t, validateWritePayload(strings.Repeat("0,0,0\n", 100)))

	// A bulk runaway of identical rows is rejected: 5-byte lines, so the
	// 64KB gate trips at count 13108.
	runaway := strings.Repeat("0,0,0\n", 30000)
	unit, count := degenerateRepetition(runaway, writeRepetitionLimits)
	require.Equal(t, "0,0,0", unit)
	require.Equal(t, 13108, count)
	require.ErrorContains(t, validateWritePayload(runaway), "corrupted payload")

	// A single long line of one repeated unit is rejected past the unit
	// thresholds.
	blob := strings.Repeat("sus ", 8192)
	unit, count = degenerateRepetition(blob, writeRepetitionLimits)
	require.Equal(t, "sus ", unit)
	require.Equal(t, 8192, count)
	require.ErrorContains(t, validateWritePayload(blob), "corrupted payload")

	// The absolute cap.
	require.ErrorContains(t, validateWritePayload(strings.Repeat("x", maxWriteBytes+1)), "byte cap")
}

func TestValidateEditSizes(t *testing.T) {
	t.Parallel()

	smallFile := strings.Repeat("a", 100)
	require.NoError(t, validateEditSizes(smallFile, []EditOperation{
		{OldString: "a", NewString: strings.Repeat("n", maxEditNewStringFileFloor)},
	}))
	err := validateEditSizes(smallFile, []EditOperation{
		{OldString: "a", NewString: strings.Repeat("n", maxEditNewStringFileFloor+1)},
	})
	require.ErrorContains(t, err, "corrupted payload")

	bigFile := strings.Repeat("a line of real content\n", 10000)
	require.NoError(t, validateEditSizes(bigFile, []EditOperation{
		{OldString: "a", NewString: strings.Repeat("n", maxEditNewStringFileRatio*len(bigFile))},
	}))
}

func TestVerifyReplacement(t *testing.T) {
	t.Parallel()

	edit := EditOperation{OldString: "foo", NewString: "bar"}
	require.NoError(t, verifyReplacement(nil, "a bar b", edit, false))
	require.ErrorContains(t, verifyReplacement(nil, "a foo b", edit, false), "still present")
	require.ErrorContains(t, verifyReplacement(nil, "a qux b", edit, false), "missing")

	// A new_string that contains old_string is a legal overlap.
	overlap := EditOperation{OldString: "foo", NewString: "foobar"}
	require.NoError(t, verifyReplacement(nil, "a foobar b", overlap, false))

	// Whitespace-corrected edits verify on normalized text, where the
	// re-indented new_string matches and the old text is gone.
	indent := EditOperation{OldString: "if (x) {", NewString: "    if (x) {"}
	require.NoError(t, verifyReplacement(nil, "\tif (x) {", indent, true))
	require.ErrorContains(t, verifyReplacement(nil, "\tfoo", EditOperation{OldString: "foo", NewString: "bar"}, true), "still present")
}

func TestApplyEditsToContentVerifiesCorrectedEdits(t *testing.T) {
	t.Parallel()

	content := "func A() {\n\treturn 1\n}\n"

	// The space-indented old_string only matches after whitespace
	// normalization, and the replacement is re-indented to the file's tabs.
	result, failed, corrected, err := applyEditsToContent(nil, content, []EditOperation{
		{OldString: "  return 1", NewString: "    return 2"},
	}, 0)
	require.NoError(t, err)
	require.Empty(t, failed)
	require.True(t, corrected)
	require.Contains(t, result, "return 2")

	// Sequential edits where one edit's new_string contains a later edit's
	// old_string must not trip the overlap guard.
	result, failed, corrected, err = applyEditsToContent(nil, "a\nb\n", []EditOperation{
		{OldString: "a", NewString: "a1"},
		{OldString: "a1", NewString: "a2"},
	}, 0)
	require.NoError(t, err)
	require.Empty(t, failed)
	require.False(t, corrected)
	require.Equal(t, "a2\nb\n", result)
}
