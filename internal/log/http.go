package log

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ResponseHeaderTimeout bounds how long a provider request waits for the
// response headers to come back. It is deliberately not an http.Client.Timeout:
// that caps the whole exchange including the streamed body, which would cut off
// a long generation mid-flight. This only covers the gap between "request sent"
// and "first byte of the response", which no healthy provider spends more than
// a few seconds on.
//
// Without it a gateway that accepts the request and then never answers -- as
// ai.eu.corti.app does intermittently for bodies over its ~64KB limit, where it
// blackholes the connection instead of returning its usual 413 -- leaves the
// agent blocked forever with no output and no error.
const ResponseHeaderTimeout = 90 * time.Second

// ProviderTransport returns the shared transport for provider traffic: the
// default transport, cloned so the timeout does not leak into every other HTTP
// user in the process, with [ResponseHeaderTimeout] applied.
//
// It is shared rather than built per call because a *http.Transport owns a
// connection pool; handing out a fresh one per request would open a new TLS
// connection every time and never reuse it.
var ProviderTransport = sync.OnceValue(func() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = ResponseHeaderTimeout
	return t
})

// LoggingTransport returns the shared provider transport wrapped in request and
// response debug logging.
var LoggingTransport = sync.OnceValue(func() http.RoundTripper {
	return &HTTPRoundTripLogger{Transport: ProviderTransport()}
})

// NewHTTPClient creates an HTTP client with debug logging enabled when debug mode is on.
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: LoggingTransport()}
}

// NewProviderHTTPClient returns the HTTP client to use for LLM provider calls.
// Requests are logged when debug is set; either way the client carries
// [ResponseHeaderTimeout], so an unresponsive endpoint surfaces as an error
// instead of hanging the agent. Providers were previously handed a client only
// in debug mode and fell back to the SDK default, which has no timeout at all.
func NewProviderHTTPClient(debug bool) *http.Client {
	if debug {
		return NewHTTPClient()
	}
	return &http.Client{Transport: ProviderTransport()}
}

// HTTPRoundTripLogger is an http.RoundTripper that logs requests and responses.
type HTTPRoundTripLogger struct {
	Transport http.RoundTripper
}

// RoundTrip implements http.RoundTripper interface with logging.
func (h *HTTPRoundTripLogger) RoundTrip(req *http.Request) (*http.Response, error) {
	var err error
	var save io.ReadCloser
	save, req.Body, err = drainBody(req.Body)
	if err != nil {
		slog.Error(
			"HTTP request failed",
			"method", req.Method,
			"url", req.URL,
			"error", err,
		)
		return nil, err
	}

	if slog.Default().Enabled(req.Context(), slog.LevelDebug) {
		slog.Debug(
			"HTTP Request",
			"method", req.Method,
			"url", req.URL,
			"body", bodyToString(save),
		)
	}

	start := time.Now()
	resp, err := h.Transport.RoundTrip(req)
	duration := time.Since(start)
	if err != nil {
		slog.Error(
			"HTTP request failed",
			"method", req.Method,
			"url", req.URL,
			"duration_ms", duration.Milliseconds(),
			"error", err,
		)
		return resp, err
	}

	save, resp.Body, err = drainBody(resp.Body)
	if err != nil {
		slog.Error("Failed to drain response body", "error", err)
		return resp, err
	}
	if slog.Default().Enabled(req.Context(), slog.LevelDebug) {
		slog.Debug(
			"HTTP Response",
			"status_code", resp.StatusCode,
			"status", resp.Status,
			"headers", formatHeaders(resp.Header),
			"body", bodyToString(save),
			"content_length", resp.ContentLength,
			"duration_ms", duration.Milliseconds(),
		)
	}
	return resp, nil
}

func bodyToString(body io.ReadCloser) string {
	if body == nil {
		return ""
	}
	src, err := io.ReadAll(body)
	if err != nil {
		slog.Error("Failed to read body", "error", err)
		return ""
	}
	var b bytes.Buffer
	if json.Indent(&b, bytes.TrimSpace(src), "", "  ") != nil {
		// not json probably
		return string(src)
	}
	return b.String()
}

// formatHeaders formats HTTP headers for logging, filtering out sensitive information.
func formatHeaders(headers http.Header) map[string][]string {
	filtered := make(map[string][]string)
	for key, values := range headers {
		lowerKey := strings.ToLower(key)
		// Filter out sensitive headers
		if strings.Contains(lowerKey, "authorization") ||
			strings.Contains(lowerKey, "api-key") ||
			strings.Contains(lowerKey, "token") ||
			strings.Contains(lowerKey, "secret") {
			filtered[key] = []string{"[REDACTED]"}
		} else {
			filtered[key] = values
		}
	}
	return filtered
}

func drainBody(b io.ReadCloser) (r1, r2 io.ReadCloser, err error) {
	if b == nil || b == http.NoBody {
		return http.NoBody, http.NoBody, nil
	}
	var buf bytes.Buffer
	if _, err = buf.ReadFrom(b); err != nil {
		return nil, b, err
	}
	if err = b.Close(); err != nil {
		return nil, b, err
	}
	return io.NopCloser(&buf), io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}
