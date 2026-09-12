package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const noisyPage = `<html>
<head><title>Docs</title><style>body{color:red}</style></head>
<body>
<nav>Home About Pricing</nav>
<h1>Install</h1>
<p>Run the installer.</p>
<script>tracker()</script>
<footer>Cookies, cookies everywhere</footer>
</body>
</html>`

func fetchTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func htmlPage(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}
}

func TestFetchURL(t *testing.T) {
	t.Parallel()

	t.Run("markdown strips the boilerplate", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(noisyPage))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Contains(t, res.Content, "# Install")
		assert.Contains(t, res.Content, "Run the installer.")
		for _, noise := range []string{"Pricing", "tracker()", "color:red", "Cookies"} {
			assert.NotContains(t, res.Content, noise)
		}
		// The converted page is markdown, not a fenced block of markdown.
		assert.False(t, strings.HasPrefix(res.Content, "```"))
	})

	t.Run("text is the visible text without markup", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(noisyPage))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatText)
		require.NoError(t, err)

		assert.Equal(t, "Install Run the installer.", res.Content)
	})

	t.Run("html returns the body markup", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(noisyPage))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatHTML)
		require.NoError(t, err)

		assert.Contains(t, res.Content, "<h1>Install</h1>")
		// html is the raw format: it keeps what the other two drop.
		assert.Contains(t, res.Content, "<nav>")
	})

	t.Run("json is indented", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"a":[1,2],"b":"c"}`))
		})
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Contains(t, res.Content, "\n  \"a\": [")
	})

	t.Run("json that does not parse is kept as it is", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`not json at all`))
		})
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Equal(t, "not json at all", res.Content)
	})

	t.Run("plain text passes through", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("# not html\n"))
		})
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Equal(t, "# not html\n", res.Content)
	})

	// A PDF or an image used to be a hard "not valid UTF-8" error, which
	// told the model nothing about what it had actually found.
	t.Run("binary responses are reported, not failed", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte{0x25, 0x50, 0x44, 0x46, 0xff, 0xfe, 0x00})
		})
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.True(t, res.Binary)
		assert.Equal(t, "application/pdf", res.ContentType)
		assert.Equal(t, 7, res.Size)
		assert.Empty(t, res.Content)
	})

	t.Run("an error status carries the body and Retry-After", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited, slow down"}`))
		})
		_, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.Error(t, err)

		var statusErr *HTTPStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusTooManyRequests, statusErr.StatusCode)
		assert.Equal(t, "30", statusErr.RetryAfter)
		assert.Contains(t, err.Error(), "rate limited, slow down")
		assert.Contains(t, err.Error(), "retry after 30")
	})

	t.Run("redirects are reported", func(t *testing.T) {
		t.Parallel()

		var srv *httptest.Server
		srv = fetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/moved" {
				http.Redirect(w, r, srv.URL+"/final", http.StatusMovedPermanently)
				return
			}
			htmlPage("<html><body><p>arrived</p></body></html>")(w, r)
		})

		res, err := FetchURL(t.Context(), srv.Client(), srv.URL+"/moved", FetchFormatMarkdown)
		require.NoError(t, err)
		assert.Equal(t, srv.URL+"/final", res.FinalURL)
		assert.Contains(t, res.Content, "arrived")
	})

	t.Run("an oversized body is truncated and says so", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(strings.Repeat("x", MaxFetchBytes+100)))
		})
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.True(t, res.Truncated)
		assert.Equal(t, MaxFetchBytes, res.Size)
	})

	t.Run("a cancelled context is an error, not a response", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(noisyPage))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := FetchURL(ctx, srv.Client(), srv.URL, FetchFormatMarkdown)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestParseFetchFormat(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want FetchFormat
		ok   bool
	}{
		{"", FetchFormatMarkdown, true},
		{"markdown", FetchFormatMarkdown, true},
		{"MarkDown", FetchFormatMarkdown, true},
		{" text ", FetchFormatText, true},
		{"html", FetchFormatHTML, true},
		{"pdf", "", false},
	} {
		got, ok := ParseFetchFormat(tt.in)
		assert.Equal(t, tt.ok, ok, tt.in)
		assert.Equal(t, tt.want, got, tt.in)
	}
}

// runFetchTool runs a tool with a session in context, since that is what
// decides where a spilled page is written.
func runFetchTool(t *testing.T, tool fantasy.AgentTool, input string) fantasy.ToolResponse {
	t.Helper()
	return runFetchToolAs(t, tool, "test-session", input)
}

func runFetchToolAs(t *testing.T, tool fantasy.AgentTool, sessionID, input string) fantasy.ToolResponse {
	t.Helper()
	ctx := context.WithValue(t.Context(), SessionIDContextKey, sessionID)
	resp, err := tool.Run(ctx, fantasy.ToolCall{Input: input})
	require.NoError(t, err)
	return resp
}

func TestFetchTool(t *testing.T) {
	t.Parallel()

	t.Run("returns converted content inline", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(noisyPage))
		tool := NewFetchTool(srv.Client())

		resp := runFetchTool(t, tool, `{"url":"`+srv.URL+`","format":"markdown"}`)
		assert.False(t, resp.IsError)
		assert.Contains(t, resp.Content, "# Install")
		assert.NotContains(t, resp.Content, "Pricing")
	})

	t.Run("format is optional and defaults to markdown", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(noisyPage))
		tool := NewFetchTool(srv.Client())

		resp := runFetchTool(t, tool, `{"url":"`+srv.URL+`"}`)
		assert.False(t, resp.IsError)
		assert.Contains(t, resp.Content, "# Install")
	})

	// The old tool truncated mid-string at 100KB. Spilling to a file is
	// what web_fetch already did, and what the model can act on. The file
	// goes to the session scratch directory, not the working directory.
	t.Run("a large page is spilled to a file", func(t *testing.T) {
		t.Parallel()

		session := "fetch-spill-test"
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(scratchRoot(), session)) })

		big := strings.Repeat("word ", MaxFetchSize/2)
		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(big))
		})
		tool := NewFetchTool(srv.Client())

		resp := runFetchToolAs(t, tool, session, `{"url":"`+srv.URL+`"}`)
		assert.False(t, resp.IsError)
		assert.Contains(t, resp.Content, "Content saved to:")
		assert.NotContains(t, resp.Content, "[Content truncated")

		pages := filepath.Join(scratchRoot(), session, fetchScratchKind)
		entries, err := os.ReadDir(pages)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		saved, err := os.ReadFile(filepath.Join(pages, entries[0].Name()))
		require.NoError(t, err)
		assert.Equal(t, big, string(saved))
		assert.True(t, strings.HasSuffix(entries[0].Name(), ".md"), entries[0].Name())
		assert.Contains(t, resp.Content, pages)
	})

	t.Run("a failing status reaches the model with its body", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("no such document"))
		})
		tool := NewFetchTool(srv.Client())

		resp := runFetchTool(t, tool, `{"url":"`+srv.URL+`"}`)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "404")
		assert.Contains(t, resp.Content, "no such document")
	})

	t.Run("rejections", func(t *testing.T) {
		t.Parallel()

		tool := NewFetchTool(http.DefaultClient)
		for _, tt := range []struct{ name, input, wants string }{
			{"no url", `{"url":""}`, "URL parameter is required"},
			{"bad format", `{"url":"https://example.com","format":"pdf"}`, "Format must be one of"},
			{"non-http scheme", `{"url":"ftp://example.com"}`, "must start with http"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				resp := runFetchTool(t, tool, tt.input)
				assert.True(t, resp.IsError)
				assert.Contains(t, resp.Content, tt.wants)
			})
		}
	})
}

func TestFetchToolIsTheOnlyFetcher(t *testing.T) {
	t.Parallel()

	// web_fetch was a second fetch tool with its own pipeline that only
	// sub-agents could reach. There is one tool now, and sub-agents get
	// it from the read-only tool set like any other read-only tool.
	srv := fetchTestServer(t, htmlPage(noisyPage))
	resp := runFetchTool(t, NewFetchTool(srv.Client()), `{"url":"`+srv.URL+`"}`)
	assert.Contains(t, resp.Content, "# Install")
}

// Downloading is a parameter on fetch rather than a tool of its own: the
// difference is only whether the bytes are read into context or written
// to a file.
func TestFetchToolDownload(t *testing.T) {
	t.Parallel()

	download := func(t *testing.T, session string, srv *httptest.Server, input string) fantasy.ToolResponse {
		t.Helper()
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(scratchRoot(), session)) })
		return runFetchToolAs(t, NewFetchTool(srv.Client()), session, input)
	}

	t.Run("saves binary content the URL names", func(t *testing.T) {
		t.Parallel()

		payload := []byte{0x50, 0x4b, 0x03, 0x04, 0xff, 0x00, 0xfe}
		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(payload)
		})

		session := "download-url-name"
		resp := download(t, session, srv, `{"url":"`+srv.URL+`/releases/tool.zip","download":true}`)
		require.False(t, resp.IsError, resp.Content)

		path := filepath.Join(scratchRoot(), session, downloadScratchKind, "tool.zip")
		assert.Contains(t, resp.Content, path)
		assert.Contains(t, resp.Content, "application/zip")
		saved, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, payload, saved)
	})

	// A download endpoint is often a path like /files/39281/download, so
	// the server's own name for the file beats the one in the URL.
	t.Run("prefers the Content-Disposition file name", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", `attachment; filename="annual report.pdf"`)
			_, _ = w.Write([]byte("%PDF-1.4"))
		})

		session := "download-header-name"
		resp := download(t, session, srv, `{"url":"`+srv.URL+`/files/39281/download","download":true}`)
		require.False(t, resp.IsError, resp.Content)

		assert.Contains(t, resp.Content, filepath.Join(session, downloadScratchKind, "annual report.pdf"))
		assert.Contains(t, resp.Content, "Content-Disposition")
	})

	t.Run("reads an RFC 5987 file name", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf`)
			_, _ = w.Write([]byte("%PDF-1.4"))
		})

		session := "download-encoded-name"
		resp := download(t, session, srv, `{"url":"`+srv.URL+`/d","download":true}`)
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "résumé.pdf")
	})

	t.Run("an explicit file_name wins over both", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Disposition", `attachment; filename="from-server.bin"`)
			_, _ = w.Write([]byte("bytes"))
		})

		session := "download-explicit-name"
		resp := download(t, session, srv, `{"url":"`+srv.URL+`/from-url.bin","download":true,"file_name":"mine.bin"}`)
		require.False(t, resp.IsError, resp.Content)

		assert.Contains(t, resp.Content, filepath.Join(session, downloadScratchKind, "mine.bin"))
		assert.NotContains(t, resp.Content, "from-server.bin")
		assert.NotContains(t, resp.Content, "Content-Disposition header")
	})

	// Neither a model nor a server may steer a write out of the scratch
	// directory.
	t.Run("path traversal is confined to the scratch directory", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Disposition", `attachment; filename="../../escaped-by-server"`)
			_, _ = w.Write([]byte("bytes"))
		})

		session := "download-traversal"
		dir := filepath.Join(scratchRoot(), session, downloadScratchKind)

		resp := download(t, session, srv, `{"url":"`+srv.URL+`/x","download":true,"file_name":"../../escaped"}`)
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, filepath.Join(dir, "escaped"))

		resp = download(t, session, srv, `{"url":"`+srv.URL+`/x","download":true}`)
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, filepath.Join(dir, "escaped-by-server"))

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Len(t, entries, 2)
	})

	t.Run("a URL with no usable name still saves", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("bytes"))
		})

		session := "download-no-name"
		resp := download(t, session, srv, `{"url":"`+srv.URL+`/","download":true}`)
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, filepath.Join(session, downloadScratchKind, "download"))
	})

	t.Run("a failing status is reported with its body", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("token expired"))
		})

		session := "download-error"
		resp := download(t, session, srv, `{"url":"`+srv.URL+`/x","download":true}`)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "403")
		assert.Contains(t, resp.Content, "token expired")
	})
}

func TestContentDispositionFileName(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ header, want string }{
		{"", ""},
		{"inline", ""},
		{`attachment; filename="report.pdf"`, "report.pdf"},
		{`attachment; filename=report.pdf`, "report.pdf"},
		{`attachment; filename="../../etc/passwd"`, "passwd"},
		{`attachment; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf`, "résumé.pdf"},
		{`attachment; filename="."`, ""},
		{"not a valid header ;;;", ""},
	} {
		assert.Equal(t, tt.want, contentDispositionFileName(tt.header), tt.header)
	}
}
