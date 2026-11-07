package log

import (
	"context"
	"io"
	"net/http"
)

type readActivityKey struct{}

// WithReadActivity returns ctx carrying onRead. A provider request made with
// the returned context calls onRead when its response headers arrive and
// again on every read of the response body that yields bytes.
//
// It sits below the SDKs on purpose. Their SSE decoders swallow keep-alives
// (Anthropic's ping event, the comment lines OpenAI-compatible gateways
// send), so a stream that is alive but between content events looks idle to
// anything above them. The bytes on the wire are the only liveness signal
// every provider shares.
func WithReadActivity(ctx context.Context, onRead func()) context.Context {
	return context.WithValue(ctx, readActivityKey{}, onRead)
}

// activityTransport reports response reads to the callback a request's
// context carries; see [WithReadActivity].
type activityTransport struct {
	base *http.Transport
}

func (t *activityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	onRead, ok := req.Context().Value(readActivityKey{}).(func())
	if !ok {
		return resp, nil
	}
	onRead()
	resp.Body = &activityBody{ReadCloser: resp.Body, onRead: onRead}
	return resp, nil
}

type activityBody struct {
	io.ReadCloser
	onRead func()
}

func (b *activityBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.onRead()
	}
	return n, err
}
