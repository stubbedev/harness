package tools

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
)

const (
	FetchToolName = "fetch"
	// MaxFetchSize is how much converted content is returned inline
	// before the page is spilled to a file instead.
	MaxFetchSize = LargeContentThreshold
)

//go:embed fetch.md.tpl
var fetchDescriptionTmpl []byte

var fetchDescriptionTpl = template.Must(
	template.New("fetchDescription").
		Parse(string(fetchDescriptionTmpl)),
)

type fetchDescriptionData struct {
	GhAvailable    bool
	MaxFetchSizeKB int
}

func fetchDescription() string {
	return renderTemplate(fetchDescriptionTpl, fetchDescriptionData{
		GhAvailable:    ghAvailable,
		MaxFetchSizeKB: MaxFetchSize / 1024,
	})
}

// maxFetchTimeoutSeconds bounds the per-call timeout parameter.
const maxFetchTimeoutSeconds = 120

func NewFetchTool(client *http.Client) fantasy.AgentTool {
	if client == nil {
		client = DefaultHTTPClient()
	}

	return fantasy.NewParallelAgentTool(
		FetchToolName,
		fetchDescription(),
		func(ctx context.Context, params FetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.URL == "" {
				return fantasy.NewTextErrorResponse("URL parameter is required"), nil
			}
			format, ok := ParseFetchFormat(params.Format)
			if !ok {
				return fantasy.NewTextErrorResponse("Format must be one of: text, markdown, html"), nil
			}
			if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
				return fantasy.NewTextErrorResponse("URL must start with http:// or https://"), nil
			}

			maxTimeout := maxFetchTimeoutSeconds
			if params.Download {
				maxTimeout = maxDownloadTimeoutSeconds
			}
			requestCtx := ctx
			if params.Timeout > 0 {
				timeout := min(params.Timeout, maxTimeout)
				var cancel context.CancelFunc
				requestCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
				defer cancel()
			}

			sessionID := GetSessionFromContext(ctx)
			if params.Download {
				return downloadToolResponse(requestCtx, downloadClient(client), sessionID, params.URL, params.FileName)
			}
			return fetchToolResponse(requestCtx, client, sessionID, params.URL, format)
		},
	)
}

// fetchToolResponse runs one fetch and renders it as a tool response: the
// content inline, or a file to read when the page is large. Shared by
// every caller, so a sub-agent fetching a page behaves exactly like the
// top-level agent doing it.
func fetchToolResponse(ctx context.Context, client *http.Client, sessionID, url string, format FetchFormat) (fantasy.ToolResponse, error) {
	res, err := FetchURL(ctx, client, url, format)
	if err != nil {
		// Preserve abort semantics when the caller cancelled the run;
		// everything else degrades to a tool error the agent can retry
		// or work around.
		if ctx.Err() != nil {
			return fantasy.ToolResponse{}, err
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to fetch URL: %s", err)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Fetched %s", url)
	if res.FinalURL != url {
		fmt.Fprintf(&b, " (redirected to %s)", res.FinalURL)
	}

	if res.Binary {
		fmt.Fprintf(&b, "\n\nThe response is not text (%s, %d bytes), so there is nothing to read here. Call fetch again with \"download\": true to save it to a file.",
			contentTypeOrUnknown(res.ContentType), res.Size)
		return fantasy.NewTextResponse(b.String()), nil
	}

	if res.Truncated {
		fmt.Fprintf(&b, "\n\n[Response truncated at %d bytes]", MaxFetchBytes)
	}

	if len(res.Content) > MaxFetchSize {
		path, err := spillFetchedContent(sessionID, res.Content, format)
		if err != nil {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to save fetched content: %s", err)), nil
		}
		fmt.Fprintf(&b, " (large page, %d bytes)\n\nContent saved to: %s\n\nUse the view and shell tools to read it.", len(res.Content), path)
		return fantasy.NewTextResponse(b.String()), nil
	}

	b.WriteString(":\n\n")
	b.WriteString(res.Content)
	return fantasy.NewTextResponse(b.String()), nil
}

// spillFetchedContent writes a large page into the session scratch
// directory so the model can view and grep it instead of carrying it in
// context. It is a working file, so it does not belong in the repository.
func spillFetchedContent(sessionID, content string, format FetchFormat) (string, error) {
	dir, err := ScratchDir(sessionID, fetchScratchKind)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "page-*"+fetchFileExtension(format))
	if err != nil {
		return "", err
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return file.Name(), nil
}

func fetchFileExtension(format FetchFormat) string {
	switch format {
	case FetchFormatHTML:
		return ".html"
	case FetchFormatText:
		return ".txt"
	default:
		return ".md"
	}
}

func contentTypeOrUnknown(contentType string) string {
	if contentType == "" {
		return "unknown content type"
	}
	return contentType
}

// fetchScratchKind is the scratch sub-directory spilled pages land in.
const fetchScratchKind = "pages"

// maxDownloadTimeoutSeconds bounds the timeout parameter when the fetch
// is a download: a large file legitimately takes minutes where a page
// does not.
const maxDownloadTimeoutSeconds = 600

// downloadClient gives a download the longer timeout unless the caller
// supplied a client of their own.
func downloadClient(client *http.Client) *http.Client {
	if client == nil || client == DefaultHTTPClient() {
		return downloadHTTPClient()
	}
	return client
}

func downloadHTTPClient() *http.Client {
	downloadClientOnce.Do(func() {
		downloadClientInstance = NewHTTPClient(5 * time.Minute)
	})
	return downloadClientInstance
}

var (
	downloadClientOnce     sync.Once
	downloadClientInstance *http.Client
)

// downloadToolResponse streams a URL to a file in the session scratch
// directory. Nothing is converted and nothing has to be text, so this is
// the path for archives, images and PDFs.
func downloadToolResponse(ctx context.Context, client *http.Client, sessionID, url, fileName string) (fantasy.ToolResponse, error) {
	dir, err := ScratchDir(sessionID, downloadScratchKind)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	res, err := StreamURLToFile(ctx, client, url, dir, fileName)
	if err != nil {
		if ctx.Err() != nil {
			return fantasy.ToolResponse{}, err
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to download from URL: %s", err)), nil
	}

	msg := fmt.Sprintf("Downloaded %d bytes from %s to %s", res.Bytes, url, res.Path)
	if res.ContentType != "" {
		msg += fmt.Sprintf(" (Content-Type: %s)", res.ContentType)
	}
	if res.NamedByServer {
		msg += "\n\nThe file name came from the server's Content-Disposition header."
	}
	return fantasy.NewTextResponse(msg), nil
}

// downloadScratchKind is the scratch sub-directory downloads land in.
const downloadScratchKind = "downloads"

// fileNameFromURL picks a file name from a URL when the caller did not
// name one. A URL with nothing usable in its path becomes "download".
func fileNameFromURL(rawURL string) string {
	name := rawURL
	if parsed, err := url.Parse(rawURL); err == nil {
		name = parsed.Path
	}
	name = path.Base(strings.TrimSuffix(name, "/"))
	if name == "" || name == "." || name == "/" {
		return "download"
	}
	return name
}
