package tools

import (
	"net/http"
	"sync"
	"time"
)

// DefaultHTTPClient is the client every web-facing tool uses when the
// caller passes none. One client, so connection pooling is shared and the
// transport settings live in a single place rather than in a copy per
// tool. Tests pass their own client instead.
func DefaultHTTPClient() *http.Client {
	defaultHTTPClientOnce.Do(func() {
		defaultHTTPClient = NewHTTPClient(30 * time.Second)
	})
	return defaultHTTPClient
}

// NewHTTPClient builds a client with the shared transport settings and a
// caller-chosen timeout. Downloads want minutes where a page fetch wants
// seconds; everything else about the two is the same.
func NewHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 90 * time.Second

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

var (
	defaultHTTPClientOnce sync.Once
	defaultHTTPClient     *http.Client
)
