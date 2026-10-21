package model

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

func freshCanvasView(u *UI) string {
	saved := u.canvas
	u.canvas = uv.ScreenBuffer{}
	content := u.View().Content
	u.canvas = saved
	return content
}

func TestView_ReusesCanvasAcrossFrames(t *testing.T) {
	t.Parallel()

	u := newFrameTestUI(t)
	u.frames = nil
	u.chat.ScrollToTop()
	u.View()
	first := u.canvas.RenderBuffer
	require.NotNil(t, first)

	_ = u.chat.ScrollBy(3)
	u.View()
	require.Same(t, first, u.canvas.RenderBuffer)
}

func TestView_ReusedCanvasMatchesFreshCanvas(t *testing.T) {
	t.Parallel()

	u := newFrameTestUI(t)
	u.frames = nil
	u.chat.ScrollToTop()

	steps := []struct {
		name string
		do   func()
	}{
		{"initial", func() {}},
		{"scroll", func() { _ = u.chat.ScrollBy(7) }},
		{"fewer messages", func() {
			u.chat.SetMessages("", frameTestItems("x")[:3]...)
			u.updateLayoutAndSize()
		}},
		{"grow", func() {
			u.width, u.height = u.width+20, u.height+10
			u.updateLayoutAndSize()
		}},
		{"shrink", func() {
			u.width, u.height = u.width-50, u.height-20
			u.updateLayoutAndSize()
		}},
		{"more messages", func() {
			u.chat.SetMessages("", frameTestItems("longer line content")...)
			u.updateLayoutAndSize()
		}},
	}
	for _, step := range steps {
		step.do()
		got := u.View().Content
		require.Equal(t, freshCanvasView(u), got, "reused canvas diverged after %s", step.name)
		require.Equal(t, uv.Rect(0, 0, u.width, u.height), u.canvas.Bounds(), "canvas size after %s", step.name)
	}
}

func TestResizeScreenBuffer(t *testing.T) {
	t.Parallel()

	var buf uv.ScreenBuffer
	resizeScreenBuffer(&buf, 4, 2)
	require.NotNil(t, buf.RenderBuffer)
	require.Equal(t, uv.Rect(0, 0, 4, 2), buf.Bounds())
	first := buf.RenderBuffer

	resizeScreenBuffer(&buf, 10, 5)
	require.Same(t, first, buf.RenderBuffer)
	require.Equal(t, uv.Rect(0, 0, 10, 5), buf.Bounds())

	resizeScreenBuffer(&buf, 3, 1)
	require.Same(t, first, buf.RenderBuffer)
	require.Equal(t, uv.Rect(0, 0, 3, 1), buf.Bounds())
}
