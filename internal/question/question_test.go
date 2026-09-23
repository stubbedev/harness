package question

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateCountsRunesNotBytes(t *testing.T) {
	t.Parallel()
	// Each CJK rune is three bytes; a text at the limit in runes is
	// well over it in bytes and must still be accepted.
	q := Question{Text: strings.Repeat("語", MaxQuestionLength), Description: "d", Type: TypeFreeText}
	require.NoError(t, q.Validate())
	q.Text += "語"
	require.Error(t, q.Validate())
}
