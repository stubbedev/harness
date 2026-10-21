package model

import uv "github.com/charmbracelet/ultraviolet"

func resizeScreenBuffer(buf *uv.ScreenBuffer, width, height int) {
	if buf.RenderBuffer == nil {
		*buf = uv.NewScreenBuffer(width, height)
		return
	}
	buf.Resize(width, height)
}
