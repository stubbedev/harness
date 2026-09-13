package image

import (
	"image"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResetCache(t *testing.T) {
	t.Parallel()

	cachedMutex.Lock()
	cachedImages[imageKey{id: "a", cols: 10, rows: 10}] = cachedImage{
		img:  image.NewRGBA(image.Rect(0, 0, 1, 1)),
		cols: 10,
		rows: 10,
	}
	cachedImages[imageKey{id: "b", cols: 20, rows: 20}] = cachedImage{
		img:  image.NewRGBA(image.Rect(0, 0, 1, 1)),
		cols: 20,
		rows: 20,
	}
	cachedMutex.Unlock()

	ResetCache()

	cachedMutex.RLock()
	length := len(cachedImages)
	cachedMutex.RUnlock()

	require.Equal(t, 0, length)
}

func TestResetIdempotent(t *testing.T) {
	t.Parallel()

	// Calling Reset on an empty cache should not panic.
	ResetCache()

	cachedMutex.RLock()
	length := len(cachedImages)
	cachedMutex.RUnlock()

	require.Equal(t, 0, length)
}

func TestFit(t *testing.T) {
	t.Parallel()

	src := image.NewRGBA(image.Rect(0, 0, 100, 50))

	t.Run("image that fits is returned unchanged", func(t *testing.T) {
		t.Parallel()

		require.Same(t, src, fit(src, 100, 50))
		require.Same(t, src, fit(src, 200, 100))
	})

	t.Run("scales down keeping the aspect ratio", func(t *testing.T) {
		t.Parallel()

		out := fit(src, 50, 50)
		require.Equal(t, 50, out.Bounds().Dx())
		require.Equal(t, 25, out.Bounds().Dy())
	})

	t.Run("scales to the other bound when it is tighter", func(t *testing.T) {
		t.Parallel()

		out := fit(src, 200, 10)
		require.Equal(t, 20, out.Bounds().Dx())
		require.Equal(t, 10, out.Bounds().Dy())
	})

	t.Run("degenerate dimensions return an empty image", func(t *testing.T) {
		t.Parallel()

		require.Empty(t, fit(src, 0, 10).Bounds().Dx())
		require.Empty(t, fit(src, 10, 0).Bounds().Dx())
	})
}
