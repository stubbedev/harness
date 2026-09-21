package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveSearchStub points the DuckDuckGo endpoint at a local server
// returning the given status and body, and restores it on cleanup.
func serveSearchStub(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	orig := ddgLiteEndpoint
	ddgLiteEndpoint = srv.URL + "/lite/?q="
	t.Cleanup(func() { ddgLiteEndpoint = orig })
}

// loadAnomalyPage reads the real bot-check payload captured from
// lite.duckduckgo.com on 2026-07-29 (HTTP 202, captcha modal).
func loadAnomalyPage(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/ddg_anomaly_202.html")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	return string(data)
}

// TestSearchRateLimitedOn202 verifies the 202 anomaly-challenge
// interstitial is reported as throttling, not parsed into an empty
// result set.
func TestSearchRateLimitedOn202(t *testing.T) {
	serveSearchStub(t, http.StatusAccepted, loadAnomalyPage(t))
	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "anything", 10)
	if !errors.Is(err, errSearchRateLimited) {
		t.Fatalf("expected errSearchRateLimited, got %v", err)
	}
}

// TestSearchRateLimitedOnAnomalyPage verifies the captcha modal is
// detected by content as well: DuckDuckGo also serves the same page
// with HTTP 200 once a client is flagged.
func TestSearchRateLimitedOnAnomalyPage(t *testing.T) {
	serveSearchStub(t, http.StatusOK, loadAnomalyPage(t))
	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "anything", 10)
	if !errors.Is(err, errSearchRateLimited) {
		t.Fatalf("expected errSearchRateLimited, got %v", err)
	}
}

// TestSearchParsesNormalResults guards against false positives: a
// genuine results page still parses.
func TestSearchParsesNormalResults(t *testing.T) {
	page := `<html><body><table>
<tr><td><a class="result-link" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpost">Example Post</a></td></tr>
<tr><td class="result-snippet">A snippet about the example post.</td></tr>
</table></body></html>`
	serveSearchStub(t, http.StatusOK, page)
	results, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "example", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Link != "https://example.com/post" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

// TestWebSearchFetchTop drives the search and the pages it links to from
// one server: the top results are fetched and previewed, a page that
// fails reports its error in place, and a long page is spilled with its
// path. ddgLiteEndpoint is package state, so like its neighbours this
// test does not run in parallel.
func TestWebSearchFetchTop(t *testing.T) {
	session := "web-search-fetch-top"
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(scratchRoot(), session)) })

	longPage := "<html><body><h1>Third</h1>" + strings.Repeat("<p>filler paragraph text</p>\n", MaxFetchSize/20) + "</body></html>"

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/lite/"):
			results := `<html><body><table>
<tr><td><a class="result-link" href="` + srv.URL + `/one">First Result</a></td></tr>
<tr><td class="result-snippet">About one.</td></tr>
<tr><td><a class="result-link" href="` + srv.URL + `/missing">Second Result</a></td></tr>
<tr><td class="result-snippet">About two.</td></tr>
<tr><td><a class="result-link" href="` + srv.URL + `/three">Third Result</a></td></tr>
<tr><td class="result-snippet">About three.</td></tr>
<tr><td><a class="result-link" href="` + srv.URL + `/four">Fourth Result</a></td></tr>
<tr><td class="result-snippet">About four.</td></tr>
</table></body></html>`
			_, _ = w.Write([]byte(results))
		case r.URL.Path == "/one":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body><h1>Page One</h1><p>Hello from one.</p></body></html>"))
		case r.URL.Path == "/three":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(longPage))
		case r.URL.Path == "/four":
			t.Errorf("the fourth result must not be fetched")
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("no such page"))
		}
	}))
	t.Cleanup(srv.Close)
	orig := ddgLiteEndpoint
	ddgLiteEndpoint = srv.URL + "/lite/?q="
	t.Cleanup(func() { ddgLiteEndpoint = orig })

	tool := NewWebSearchTool(srv.Client())
	// fetch_top is clamped to three, so the fourth result is never fetched.
	resp := runFetchToolAs(t, tool, session, `{"query":"anything","fetch_top":5}`)
	require.False(t, resp.IsError, resp.Content)

	assert.Contains(t, resp.Content, "Found 4 search results:")
	assert.Contains(t, resp.Content, "4. Fourth Result")
	assert.Contains(t, resp.Content, "Top 3 pages:")

	assert.Contains(t, resp.Content, "### 1. First Result\nURL: "+srv.URL+"/one\n# Page One\n\nHello from one.")
	assert.Contains(t, resp.Content, "Hello from one.\n\n### 2. Second Result")

	assert.Contains(t, resp.Content, "### 2. Second Result\nURL: "+srv.URL+"/missing\nError: request failed with status 404")
	assert.Contains(t, resp.Content, "no such page")

	assert.Contains(t, resp.Content, "### 3. Third Result\nURL: "+srv.URL+"/three\nFull page (")
	assert.Contains(t, resp.Content, "saved to: "+filepath.Join(scratchRoot(), session, fetchScratchKind))
	assert.Contains(t, resp.Content, "# Third\n\nfiller paragraph text")
	assert.Contains(t, resp.Content, "more bytes; fetch the URL for the rest]")

	// The preview is a preview: the long page does not ride in whole.
	assert.Less(t, len(resp.Content), 3*fetchTopPreviewBytes+2048)
}

// TestWebSearchWithoutFetchTop guards the default: no fetch_top, no
// pages fetched.
func TestWebSearchWithoutFetchTop(t *testing.T) {
	page := `<html><body><table>
<tr><td><a class="result-link" href="http://127.0.0.1:1/never">Never Fetched</a></td></tr>
<tr><td class="result-snippet">A snippet.</td></tr>
</table></body></html>`
	serveSearchStub(t, http.StatusOK, page)

	resp := runFetchTool(t, NewWebSearchTool(http.DefaultClient), `{"query":"anything"}`)
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "1. Never Fetched")
	assert.NotContains(t, resp.Content, "Top ")
}
