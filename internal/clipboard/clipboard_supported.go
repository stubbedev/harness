//go:build (darwin || linux || windows || freebsd || openbsd || netbsd) && !ios && !android

package clipboard

import (
	"bytes"
	"context"
	"time"

	"golang.design/x/clipboard"
)

// ready reports whether the native clipboard is usable. Touching the clipboard
// after a failed initialization may panic, and golang.design's Init is
// idempotent and cheap once it has run, so every entry point asks it again
// rather than tracking initialization state of our own.
func ready() bool {
	return clipboard.Init() == nil
}

func initClipboard() error {
	return clipboard.Init()
}

func writeText(text string) error {
	if !ready() {
		return ErrUnsupported
	}
	// A write error means the backend never took the clipboard; reading back
	// catches the rest, where the write is accepted but the text is not served
	// afterwards. Neither check subsumes the other: a failed write leaves an
	// earlier identical copy in place, which reads back as a success.
	if _, err := clipboard.Write(context.Background(), clipboard.FmtText, []byte(text)); err != nil {
		return ErrWriteFailed
	}
	// Clipboard managers (wl-clip-persist, clipman, GPaste, ...) take over the
	// selection right after a write to cache the content. An immediate read
	// can race that takeover and catch an empty or stale selection even though
	// the copy landed, so give the manager a moment before declaring failure.
	for range 4 {
		data, err := clipboard.Read(context.Background(), clipboard.FmtText)
		if err == nil && bytes.Equal(data, []byte(text)) {
			return nil
		}
		time.Sleep(30 * time.Millisecond)
	}
	return ErrWriteFailed
}

func read(f Format) ([]byte, error) {
	if !ready() {
		return nil, ErrUnsupported
	}
	var format clipboard.Format
	switch f {
	case FormatText:
		format = clipboard.FmtText
	case FormatImage:
		format = clipboard.FmtImage
	default:
		return nil, ErrEmpty
	}
	data, err := clipboard.Read(context.Background(), format)
	if err != nil || data == nil {
		return nil, ErrEmpty
	}
	return data, nil
}
