package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
		// The path alone left the model a round trip short of knowing
		// what it had; the start of the page rides along with it.
		assert.Contains(t, resp.Content, "Preview:\nword word")
		assert.Less(t, len(resp.Content), MaxToolPreviewBytes+1024)

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

// numberedLines is a text body of n lines, "line 1" through "line n",
// for tests that slice or search by line.
func numberedLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func plainText(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	}
}

// Paging and searching a page are fetch parameters, so a model can read
// a long page piecemeal without a shell round trip through a spill file.
func TestFetchToolView(t *testing.T) {
	t.Parallel()

	t.Run("offset and limit slice by lines", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, plainText(numberedLines(50)))
		resp := runFetchTool(t, NewFetchTool(srv.Client()), `{"url":"`+srv.URL+`","offset":10,"limit":5}`)
		require.False(t, resp.IsError, resp.Content)

		assert.Contains(t, resp.Content, "[lines 11-15 of 50]")
		assert.Contains(t, resp.Content, "line 11\n")
		assert.Contains(t, resp.Content, "line 15")
		assert.NotContains(t, resp.Content, "line 10\n")
		assert.NotContains(t, resp.Content, "line 16")
		assert.Contains(t, resp.Content, "(35 more lines. Call again with offset=15")
	})

	t.Run("an offset past the end says so", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, plainText(numberedLines(5)))
		resp := runFetchTool(t, NewFetchTool(srv.Client()), `{"url":"`+srv.URL+`","offset":99}`)
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, "offset 99 is past the end: the page has 5 lines")
	})

	t.Run("find returns matches with context and line numbers", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, plainText(numberedLines(50)))
		resp := runFetchTool(t, NewFetchTool(srv.Client()), `{"url":"`+srv.URL+`","find":"line (20|30)$"}`)
		require.False(t, resp.IsError, resp.Content)

		assert.Contains(t, resp.Content, `2 lines match "line (20|30)$"`)
		assert.Contains(t, resp.Content, "18|line 18\n19|line 19\n20|line 20\n21|line 21\n22|line 22\n--\n28|line 28")
		assert.Contains(t, resp.Content, "32|line 32")
		assert.NotContains(t, resp.Content, "23|")
		assert.NotContains(t, resp.Content, "more matches")
	})

	t.Run("find caps the matches and numbers from the offset", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, plainText(numberedLines(200)))
		resp := runFetchTool(t, NewFetchTool(srv.Client()), `{"url":"`+srv.URL+`","offset":100,"find":"line"}`)
		require.False(t, resp.IsError, resp.Content)

		assert.Contains(t, resp.Content, `100 lines match "line"`)
		assert.Contains(t, resp.Content, "101|line 101\n")
		assert.Contains(t, resp.Content, "... and 40 more matches")
		assert.NotContains(t, resp.Content, "|line 100\n")
	})

	t.Run("find with nothing matching says so", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, plainText(numberedLines(5)))
		resp := runFetchTool(t, NewFetchTool(srv.Client()), `{"url":"`+srv.URL+`","find":"nowhere"}`)
		require.False(t, resp.IsError, resp.Content)
		assert.Contains(t, resp.Content, `No lines match "nowhere".`)
	})

	t.Run("a bad find pattern is a tool error", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, plainText(numberedLines(5)))
		resp := runFetchTool(t, NewFetchTool(srv.Client()), `{"url":"`+srv.URL+`","find":"("}`)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "Invalid find pattern")
	})

	// A slice of a large page is what the model asked for, so it comes
	// back whole alongside the path; only an unsliced page is previewed.
	t.Run("a slice of a large page returns the slice and the path", func(t *testing.T) {
		t.Parallel()

		session := "fetch-view-spill-test"
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(scratchRoot(), session)) })

		srv := fetchTestServer(t, plainText(numberedLines(MaxFetchSize/4)))
		resp := runFetchToolAs(t, NewFetchTool(srv.Client()), session, `{"url":"`+srv.URL+`","offset":5000,"limit":3}`)
		require.False(t, resp.IsError, resp.Content)

		assert.Contains(t, resp.Content, "Content saved to:")
		assert.NotContains(t, resp.Content, "Preview:")
		assert.Contains(t, resp.Content, "[lines 5001-5003 of")
		assert.Contains(t, resp.Content, "line 5001\nline 5002\nline 5003")
	})
}

// A page's bytes are decoded from whatever charset it declares, and a
// server that mislabels HTML as plain text still gets it converted.
func TestFetchURLDecoding(t *testing.T) {
	t.Parallel()

	t.Run("a declared Latin-1 page is decoded", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=iso-8859-1")
			_, _ = w.Write([]byte("caf\xe9 cr\xe8me"))
		})
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.False(t, res.Binary)
		assert.Equal(t, "café crème", res.Content)
	})

	t.Run("a meta charset is honoured on HTML", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><meta charset="shift_jis"></head><body><p>` + "\x93\xfa\x96\x7b\x8c\xea" + `</p></body></html>`))
		})
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatText)
		require.NoError(t, err)

		assert.False(t, res.Binary)
		assert.Equal(t, "日本語", res.Content)
	})

	// The UTF-8 guess is made on the first kilobyte, so a page whose only
	// non-ASCII byte comes later used to be reported as binary.
	t.Run("an undeclared non-UTF-8 text page is still text", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, plainText(strings.Repeat("a", 2048)+" caf\xe9"))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.False(t, res.Binary)
		assert.True(t, strings.HasSuffix(res.Content, " café"), res.Content[len(res.Content)-10:])
	})

	t.Run("HTML served as text/plain is converted", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, plainText("\n  "+noisyPage))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Equal(t, "text/html", res.ContentType)
		assert.Contains(t, res.Content, "# Install")
		assert.NotContains(t, res.Content, "<h1>")
		assert.NotContains(t, res.Content, "Pricing")
	})

	t.Run("HTML with no content type keeps the declared charset when sniffed", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream; charset=iso-8859-1")
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body><p>caf\xe9</p></body></html>"))
		})
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatText)
		require.NoError(t, err)

		assert.False(t, res.Binary)
		assert.Equal(t, "café", res.Content)
	})

	// The sniff only promotes a body that is HTML; other bytes behind an
	// octet-stream type are still binary.
	t.Run("binary that is not HTML stays binary", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0xff, 0xfe})
		})
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)
		assert.True(t, res.Binary)
	})
}

func TestSniffContentType(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ header, body, want string }{
		{"", "<!DOCTYPE html><html>", "text/html"},
		{"", "\n\t <HTML lang=en>", "text/html"},
		{"text/plain", "<html>", "text/html"},
		{"text/plain; charset=iso-8859-1", "<body>", "text/html; charset=iso-8859-1"},
		{"application/octet-stream", "<head>", "text/html"},
		{"text/plain", "# not html", "text/plain"},
		{"application/json", "<html>", "application/json"},
		{"text/html; charset=utf-8", "plain", "text/html; charset=utf-8"},
		{"", "", ""},
	} {
		assert.Equal(t, tt.want, sniffContentType(tt.header, []byte(tt.body)), "%q / %q", tt.header, tt.body)
	}
}

// Markdown conversion keeps what a model reads a docs page for: the
// tables, the code, links it can follow, and the article rather than the
// chrome around it.
func TestFetchURLMarkdown(t *testing.T) {
	t.Parallel()

	t.Run("a table renders as a GFM table", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(`<html><body>
<table><thead><tr><th>Flag</th><th>Meaning</th></tr></thead>
<tbody><tr><td>-v</td><td>verbose</td></tr><tr><td>-q</td><td>quiet</td></tr></tbody></table>
</body></html>`))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Contains(t, res.Content, "| Flag | Meaning |")
		assert.Contains(t, res.Content, "| --- | --- |")
		assert.Contains(t, res.Content, "| -v | verbose |")
	})

	t.Run("code blocks are fenced", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(`<html><body><p>Run:</p><pre><code class="language-sh">go build ./...
go test ./...</code></pre></body></html>`))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Contains(t, res.Content, "```sh\ngo build ./...\ngo test ./...\n```")
	})

	t.Run("relative links and images become absolute", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(`<html><body>
<p><a href="/docs/install">Install</a> or <a href="../faq#top">FAQ</a> or <a href="https://example.com/x">elsewhere</a>.</p>
<img src="img/logo.png" alt="logo">
</body></html>`))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL+"/guide/start/", FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Contains(t, res.Content, "[Install]("+srv.URL+"/docs/install)")
		assert.Contains(t, res.Content, "[FAQ]("+srv.URL+"/guide/faq#top)")
		assert.Contains(t, res.Content, "[elsewhere](https://example.com/x)")
		assert.Contains(t, res.Content, "![logo]("+srv.URL+"/guide/start/img/logo.png)")
	})

	t.Run("a base element wins over the page URL", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(`<html><head><base href="https://cdn.example.org/v2/"></head>
<body><a href="api/">API</a></body></html>`))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL+"/guide/", FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Contains(t, res.Content, "[API](https://cdn.example.org/v2/api/)")
	})

	t.Run("an article inside main keeps its header while the site chrome goes", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(`<html><body>
<header><a href="/">Site Logo</a><nav>Docs Blog Pricing</nav></header>
<aside>Related: other posts</aside>
<main><article>
<header><h1>Configuring the Thing</h1><p>Posted yesterday</p></header>
<p>The thing is configured with a file. This paragraph is the body of the article and is long enough to be most of the page's text.</p>
</article></main>
<footer>Copyright Footer</footer>
</body></html>`))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Contains(t, res.Content, "# Configuring the Thing")
		assert.Contains(t, res.Content, "Posted yesterday")
		assert.Contains(t, res.Content, "configured with a file")
		for _, chrome := range []string{"Site Logo", "Pricing", "Related", "Copyright"} {
			assert.NotContains(t, res.Content, chrome)
		}
	})

	// Without semantic tags for the chrome, the main element is still the
	// place to start when it holds the page.
	t.Run("main is the root when it holds the page", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(`<html><body>
<div class="banner">Sign up for our newsletter</div>
<main><h1>Guide</h1><p>Everything you need to know about the guide, in a paragraph long enough to dominate the page.</p></main>
<div class="cookie">We use cookies</div>
</body></html>`))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Contains(t, res.Content, "# Guide")
		assert.NotContains(t, res.Content, "newsletter")
		assert.NotContains(t, res.Content, "cookies")
	})

	t.Run("a thin main is not the root", func(t *testing.T) {
		t.Parallel()

		srv := fetchTestServer(t, htmlPage(`<html><body>
<div><h1>The Real Content</h1><p>Lives outside main on this page, in a long paragraph that is most of what there is to read here.</p></div>
<main><p>Teaser</p></main>
</body></html>`))
		res, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.NoError(t, err)

		assert.Contains(t, res.Content, "# The Real Content")
		assert.Contains(t, res.Content, "Teaser")
	})
}

// A bot filter or a rate limiter often lets a second browser through;
// one retry is cheap and turns a dead end into a page.
func TestFetchURLRetry(t *testing.T) {
	t.Parallel()

	type seen struct {
		userAgent string
		referer   string
	}
	record := func(t *testing.T, firstStatus int, secondStatus int) (*httptest.Server, func() []seen) {
		t.Helper()
		var mu sync.Mutex
		var requests []seen
		srv := fetchTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requests = append(requests, seen{r.Header.Get("User-Agent"), r.Header.Get("Referer")})
			n := len(requests)
			mu.Unlock()

			status := secondStatus
			if n == 1 {
				status = firstStatus
			}
			if status != http.StatusOK {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("blocked"))
				return
			}
			htmlPage("<html><body><p>let in</p></body></html>")(w, r)
		})
		return srv, func() []seen {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(requests)
		}
	}

	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("a %d then a 200 succeeds", status), func(t *testing.T) {
			t.Parallel()

			srv, requests := record(t, status, http.StatusOK)
			res, err := FetchURL(t.Context(), srv.Client(), srv.URL+"/page", FetchFormatMarkdown)
			require.NoError(t, err)
			assert.Contains(t, res.Content, "let in")

			got := requests()
			require.Len(t, got, 2)
			assert.Equal(t, BrowserUserAgent, got[0].userAgent)
			assert.Empty(t, got[0].referer)
			assert.NotEqual(t, got[0].userAgent, got[1].userAgent)
			assert.Contains(t, userAgents, got[1].userAgent)
			assert.Equal(t, srv.URL+"/", got[1].referer)
		})
	}

	t.Run("a persistent 403 is reported after one retry", func(t *testing.T) {
		t.Parallel()

		srv, requests := record(t, http.StatusForbidden, http.StatusForbidden)
		_, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)

		var statusErr *HTTPStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusForbidden, statusErr.StatusCode)
		assert.Contains(t, err.Error(), "blocked")
		assert.Len(t, requests(), 2)
	})

	t.Run("a 404 is not retried", func(t *testing.T) {
		t.Parallel()

		srv, requests := record(t, http.StatusNotFound, http.StatusOK)
		_, err := FetchURL(t.Context(), srv.Client(), srv.URL, FetchFormatMarkdown)
		require.Error(t, err)
		assert.Len(t, requests(), 1)
	})
}

func TestFetchRetryDelay(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		header string
		want   time.Duration
	}{
		{"", fetchRetryDefaultDelay},
		{"0", 0},
		{"3", 3 * time.Second},
		{"3600", fetchRetryMaxDelay},
		{"-5", 0},
		{"soon", fetchRetryDefaultDelay},
		{time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat), 0},
	} {
		assert.Equal(t, tt.want, fetchRetryDelay(tt.header), tt.header)
	}

	// An HTTP-date in the future is honoured up to the cap.
	future := fetchRetryDelay(time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	assert.Equal(t, fetchRetryMaxDelay, future)
}

func TestRewriteGitHubBlobURL(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ in, want string }{
		{
			"https://github.com/stubbedev/harness/blob/main/AGENTS.md",
			"https://raw.githubusercontent.com/stubbedev/harness/main/AGENTS.md",
		},
		{
			"https://www.github.com/o/r/blob/v1.2.3/internal/agent/tools/fetch.go",
			"https://raw.githubusercontent.com/o/r/v1.2.3/internal/agent/tools/fetch.go",
		},
		{
			// The query and fragment are the web UI's, not the file's.
			"https://github.com/o/r/blob/abc123/README.md?plain=1#L10",
			"https://raw.githubusercontent.com/o/r/abc123/README.md",
		},
		// Not a file page: unchanged.
		{"https://github.com/o/r", "https://github.com/o/r"},
		{"https://github.com/o/r/tree/main/docs", "https://github.com/o/r/tree/main/docs"},
		{"https://github.com/o/r/blob/main", "https://github.com/o/r/blob/main"},
		{"https://github.com/o/r/blob/main/", "https://github.com/o/r/blob/main/"},
		{"https://github.com/o/r/pull/1/files", "https://github.com/o/r/pull/1/files"},
		{"https://gitlab.com/o/r/blob/main/x", "https://gitlab.com/o/r/blob/main/x"},
		{"https://raw.githubusercontent.com/o/r/main/x", "https://raw.githubusercontent.com/o/r/main/x"},
		{"not a url", "not a url"},
	} {
		assert.Equal(t, tt.want, rewriteGitHubBlobURL(tt.in), tt.in)
	}
}

func TestFetchURLRewritesGitHubBlob(t *testing.T) {
	t.Parallel()

	// The rewrite happens before the request, so a client that only knows
	// the raw host is what proves it: the transport rewrites raw.
	// githubusercontent.com to the test server and refuses anything else.
	srv := fetchTestServer(t, plainText("# from raw\n"))
	client := &http.Client{Transport: hostRewritingTransport{t: t, srv: srv, wantHost: "raw.githubusercontent.com"}}

	res, err := FetchURL(t.Context(), client, "https://github.com/o/r/blob/main/README.md", FetchFormatMarkdown)
	require.NoError(t, err)
	assert.Equal(t, "# from raw\n", res.Content)
	assert.Equal(t, "https://raw.githubusercontent.com/o/r/main/README.md", res.FinalURL)
}

// hostRewritingTransport sends requests for wantHost to a test server and
// fails the test for any other host.
type hostRewritingTransport struct {
	t        *testing.T
	srv      *httptest.Server
	wantHost string
}

func (h hostRewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != h.wantHost {
		h.t.Errorf("request went to %s, want %s", req.URL.Host, h.wantHost)
		return nil, fmt.Errorf("unexpected host %s", req.URL.Host)
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(h.srv.URL, "http://")
	resp, err := h.srv.Client().Transport.RoundTrip(clone)
	if resp != nil {
		// The client reports the request it made, not the one the
		// transport rewrote.
		resp.Request = req
	}
	return resp, err
}
