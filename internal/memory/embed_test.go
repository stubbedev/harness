package memory

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbedIsDeterministicAndNormalized(t *testing.T) {
	t.Parallel()

	a := Embed("The diagnostics relay sweep runs at turn end")
	b := Embed("The diagnostics relay sweep runs at turn end")
	require.Equal(t, a, b)

	sum := 0.0
	for _, v := range a {
		sum += float64(v) * float64(v)
	}
	require.InDelta(t, 1, sum, 1e-4)

	require.Equal(t, make([]float32, embedDim), Embed("   "))
}

func TestEmbedCapturesMorphology(t *testing.T) {
	t.Parallel()

	running := Embed("running")
	runs := Embed("runs")
	zebra := Embed("zebra")

	same := cosine(running, runs)
	unrelated := cosine(running, zebra)
	require.Greater(t, same, unrelated, "morphological variants must embed closer than unrelated words")
	require.Greater(t, same, 0.05)
}

func TestEmbeddingRoundTrip(t *testing.T) {
	t.Parallel()

	vec := Embed("keep tests offline with a scripted model")
	decoded := decodeEmbedding(encodeEmbedding(vec))
	require.Equal(t, vec, decoded)

	require.Nil(t, decodeEmbedding(nil))
	require.Nil(t, decodeEmbedding(encodeEmbedding(vec)[:4*embedDim-1]))
}
