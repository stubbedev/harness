package message

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatRefParsesBack pins the round trip: everything FormatRef
// produces parses back to the same values, because the editor's tokens,
// the transcript's tags and the editor's backspace recognition all
// share these two functions.
func TestFormatRefParsesBack(t *testing.T) {
	t.Parallel()

	for kind := range map[string]struct{}{RefKindImage: {}, RefKindFile: {}, RefKindPaste: {}} {
		ref := FormatRef(kind, 2, 0)
		assert.Contains(t, ref, "#2")
		gotKind, n, lines, ok := ParseRef(ref)
		require.True(t, ok, "%q must parse", ref)
		assert.Equal(t, kind, gotKind)
		assert.Equal(t, 2, n)
		assert.Equal(t, 0, lines)
	}

	// The line suffix is a paste-only shape.
	ref := FormatRef(RefKindPaste, 2, 24)
	gotKind, n, lines, ok := ParseRef(ref)
	require.True(t, ok, "%q must parse", ref)
	assert.Equal(t, RefKindPaste, gotKind)
	assert.Equal(t, 2, n)
	assert.Equal(t, 24, lines)
}

// TestParseRefRejectsNonReferences pins the shape: anything that is not
// a bracket reference does not parse, and a line-count suffix on an
// image or file kind is rejected.
func TestParseRef(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"", "[", "]", "[]", "[Image #0]", "[Image #x]", "[Image]",
		"plain text", "a [Image #1] b", "[Unknown #1]",
		"[Image #1 +2 lines]", "[File #1 +2 lines]",
		"[Pasted text #1 +x lines]", "[Pasted text #1 +0 lines]",
	} {
		_, _, _, ok := ParseRef(s)
		assert.False(t, ok, "%q must not parse", s)
	}

	_, n, lines, ok := ParseRef("[Pasted text #1]")
	require.True(t, ok)
	assert.Equal(t, 1, n)
	assert.Equal(t, 0, lines)
}

// TestRefKind pins the MIME classification behind the kind names.
func TestRefKind(t *testing.T) {
	t.Parallel()

	assert.Equal(t, RefKindImage, Attachment{MimeType: "image/png"}.RefKind())
	assert.Equal(t, RefKindPaste, Attachment{MimeType: "text/plain"}.RefKind())
	assert.Equal(t, RefKindFile, Attachment{MimeType: "application/pdf"}.RefKind())
}
