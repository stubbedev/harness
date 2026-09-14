package log

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPRoundTripLogger(t *testing.T) {
	// Create a test server that returns a 500 error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Header", "test-value")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "Internal server error", "code": 500}`))
	}))
	defer server.Close()

	// Create HTTP client with logging
	client := NewHTTPClient()

	// Make a request
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		server.URL,
		strings.NewReader(`{"test": "data"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Verify response
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status code 500, got %d", resp.StatusCode)
	}
}

func TestFormatHeaders(t *testing.T) {
	headers := http.Header{
		"Content-Type":  []string{"application/json"},
		"Authorization": []string{"Bearer secret-token"},
		"X-API-Key":     []string{"api-key-123"},
		"User-Agent":    []string{"test-agent"},
	}

	formatted := formatHeaders(headers)

	// Check that sensitive headers are redacted
	if formatted["Authorization"][0] != "[REDACTED]" {
		t.Error("Authorization header should be redacted")
	}
	if formatted["X-API-Key"][0] != "[REDACTED]" {
		t.Error("X-API-Key header should be redacted")
	}

	// Check that non-sensitive headers are preserved
	if formatted["Content-Type"][0] != "application/json" {
		t.Error("Content-Type header should be preserved")
	}
	if formatted["User-Agent"][0] != "test-agent" {
		t.Error("User-Agent header should be preserved")
	}
}

// A gateway that accepts the request and then never writes a response used to
// hang the agent forever, because provider clients carried no timeout of any
// kind. Both provider clients must give up once ResponseHeaderTimeout elapses.
func TestProviderHTTPClientTimesOutOnSilentServer(t *testing.T) {
	t.Parallel()

	for _, debug := range []bool{false, true} {
		t.Run(map[bool]string{false: "quiet", true: "debug"}[debug], func(t *testing.T) {
			t.Parallel()

			released := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				<-released // accept the request, never respond
			}))
			t.Cleanup(func() {
				close(released)
				server.Close()
			})

			client := NewProviderHTTPClient(debug)
			// Swap in a short timeout so the test does not wait the full
			// ResponseHeaderTimeout; the wiring under test is the same.
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.ResponseHeaderTimeout = 200 * time.Millisecond
			if debug {
				client.Transport = &HTTPRoundTripLogger{Transport: transport}
			} else {
				client.Transport = transport
			}

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() {
				resp, err := client.Do(req)
				if resp != nil {
					resp.Body.Close()
				}
				done <- err
			}()

			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected a timeout error, got a response")
				}
				if !strings.Contains(err.Error(), "timeout awaiting response headers") {
					t.Fatalf("expected a response-header timeout, got %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("client hung instead of timing out")
			}
		})
	}
}

// The provider transport must be shared, so its connection pool is reused
// rather than rebuilt per request, and must not mutate http.DefaultTransport.
func TestProviderTransportIsSharedAndIsolated(t *testing.T) {
	t.Parallel()

	if ProviderTransport() != ProviderTransport() {
		t.Fatal("ProviderTransport must return the same shared transport")
	}
	if got := http.DefaultTransport.(*http.Transport).ResponseHeaderTimeout; got != 0 {
		t.Fatalf("http.DefaultTransport was mutated: ResponseHeaderTimeout = %v", got)
	}
	if got := ProviderTransport().(*http.Transport).ResponseHeaderTimeout; got != ResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", got, ResponseHeaderTimeout)
	}

	// Both provider clients must actually reach that transport, in debug mode
	// through the logger and otherwise directly.
	if got := NewProviderHTTPClient(false).Transport; got != ProviderTransport() {
		t.Fatalf("quiet client transport = %T, want the shared provider transport", got)
	}
	logged, ok := NewProviderHTTPClient(true).Transport.(*HTTPRoundTripLogger)
	if !ok {
		t.Fatalf("debug client transport = %T, want *HTTPRoundTripLogger", NewProviderHTTPClient(true).Transport)
	}
	if logged.Transport != ProviderTransport() {
		t.Fatal("debug client logs over a transport other than the shared provider transport")
	}

	// An http.Client.Timeout would cap the streamed body too, cutting long
	// generations off mid-answer; the bound belongs on the headers alone.
	for _, c := range []*http.Client{NewProviderHTTPClient(false), NewProviderHTTPClient(true), NewHTTPClient()} {
		if c.Timeout != 0 {
			t.Fatalf("client has Client.Timeout = %v, which would truncate streams", c.Timeout)
		}
	}
}
