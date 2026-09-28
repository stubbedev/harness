package lsp

import (
	"testing"

	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/stretchr/testify/require"
)

func TestCharacterOffset(t *testing.T) {
	t.Parallel()

	// "é" is two bytes and one UTF-16 unit; "𝄞" is four bytes and two.
	line := "é𝄞 := Symbol"
	byteOffset := len("é𝄞 := ")
	require.Equal(t, byteOffset, CharacterOffset(line, byteOffset, powernap.UTF8))
	require.Equal(t, 1+2+4, CharacterOffset(line, byteOffset, powernap.UTF16))
	require.Equal(t, 1+1+4, CharacterOffset(line, byteOffset, powernap.UTF32))
	require.Equal(t, len(line), CharacterOffset(line, 1000, powernap.UTF8))
}
