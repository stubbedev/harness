package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// BrowserUserAgent is a realistic browser User-Agent. A fair number of
// sites refuse a bare tool UA outright.
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// MaxFetchBytes caps how much of a response is read. Anything beyond this
// is dropped and the result is marked truncated.
const MaxFetchBytes = 5 * 1024 * 1024 // 5MB

// errorBodySnippetLimit is how much of a non-2xx body is quoted back. A
// 4xx payload usually says what was wrong; the whole page rarely does.
const errorBodySnippetLimit = 512

var multipleNewlinesRe = regexp.MustCompile(`\n{3,}`)

// FetchFormat is how a fetched document is rendered for the model.
type FetchFormat string

const (
	// FetchFormatMarkdown strips boilerplate and converts to markdown. It
	// is what a model wants from a documentation page.
	FetchFormatMarkdown FetchFormat = "markdown"
	// FetchFormatText is the visible text with the markup dropped.
	FetchFormatText FetchFormat = "text"
	// FetchFormatHTML is the document's body markup, unconverted.
	FetchFormatHTML FetchFormat = "html"
)

// ParseFetchFormat maps a caller-supplied format name onto a FetchFormat.
// An empty name is markdown, which is the useful default.
func ParseFetchFormat(name string) (FetchFormat, bool) {
	switch FetchFormat(strings.ToLower(strings.TrimSpace(name))) {
	case "":
		return FetchFormatMarkdown, true
	case FetchFormatMarkdown:
		return FetchFormatMarkdown, true
	case FetchFormatText:
		return FetchFormatText, true
	case FetchFormatHTML:
		return FetchFormatHTML, true
	default:
		return "", false
	}
}

// FetchResult is one fetched document, already converted to the requested
// format.
type FetchResult struct {
	// Content is the converted document, empty when Binary.
	Content string
	// ContentType is the response's declared media type.
	ContentType string
	// FinalURL is where the request ended up; it differs from the
	// requested URL when the server redirected.
	FinalURL string
	// Truncated reports that the response was longer than MaxFetchBytes.
	Truncated bool
	// Binary reports a response that is not text, so nothing was
	// converted. The caller should point the model at `download`.
	Binary bool
	// Size is how many bytes were read from the body.
	Size int
}

// HTTPStatusError is a non-2xx response. It carries a snippet of the body
// because an API's 4xx payload usually explains the failure, and throwing
// it away leaves the model guessing.
type HTTPStatusError struct {
	StatusCode int
	Status     string
	Snippet    string
	RetryAfter string
}

func (e *HTTPStatusError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "request failed with status %s", e.Status)
	if e.RetryAfter != "" {
		fmt.Fprintf(&b, " (retry after %s)", e.RetryAfter)
	}
	if e.Snippet != "" {
		fmt.Fprintf(&b, ": %s", e.Snippet)
	}
	return b.String()
}

// FetchURL fetches a URL and renders it in the requested format. It is the
// single fetch path: every web-fetching tool goes through it, so they all
// get the boilerplate stripping, the JSON formatting and the error detail.
func FetchURL(ctx context.Context, client *http.Client, url string, format FetchFormat) (FetchResult, error) {
	if client == nil {
		client = DefaultHTTPClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", BrowserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return FetchResult{}, err
	}
	defer resp.Body.Close()

	result := FetchResult{
		ContentType: resp.Header.Get("Content-Type"),
		FinalURL:    url,
	}
	if resp.Request != nil && resp.Request.URL != nil {
		result.FinalURL = resp.Request.URL.String()
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodySnippetLimit))
		return result, &HTTPStatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Snippet:    strings.TrimSpace(collapseWhitespace(string(snippet))),
			RetryAfter: resp.Header.Get("Retry-After"),
		}
	}

	// One byte past the cap tells truncation from a body that happens to
	// be exactly MaxFetchBytes long.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchBytes+1))
	if err != nil {
		return result, fmt.Errorf("failed to read response body: %w", err)
	}
	if len(body) > MaxFetchBytes {
		body = body[:MaxFetchBytes]
		result.Truncated = true
	}
	result.Size = len(body)

	content := string(body)
	if !utf8.ValidString(content) {
		// A PDF or an image is not an error: say what it is and let the
		// model decide to download it instead.
		result.Binary = true
		return result, nil
	}

	result.Content, err = convertFetchedContent(content, result.ContentType, format)
	if err != nil {
		return result, err
	}
	return result, nil
}

// convertFetchedContent renders a text response in the requested format.
// Non-HTML content is returned as-is, except JSON, which is indented.
func convertFetchedContent(content, contentType string, format FetchFormat) (string, error) {
	if isHTMLContentType(contentType) {
		switch format {
		case FetchFormatText:
			text, err := extractTextFromHTML(content)
			if err != nil {
				return "", fmt.Errorf("failed to extract text from HTML: %w", err)
			}
			return text, nil
		case FetchFormatHTML:
			body, err := extractHTMLBody(content)
			if err != nil {
				return "", fmt.Errorf("failed to extract body from HTML: %w", err)
			}
			return body, nil
		default:
			markdown, err := ConvertHTMLToMarkdown(removeNoisyElements(content))
			if err != nil {
				return "", fmt.Errorf("failed to convert HTML to markdown: %w", err)
			}
			return cleanupMarkdown(markdown), nil
		}
	}

	if isJSONContentType(contentType) {
		// A response that claims JSON but does not parse stays as it is:
		// the model can still read it, and an error here would lose it.
		if formatted, err := FormatJSON(content); err == nil {
			return formatted, nil
		}
	}
	return content, nil
}

func isHTMLContentType(contentType string) bool {
	lower := strings.ToLower(contentType)
	return strings.Contains(lower, "text/html") || strings.Contains(lower, "application/xhtml+xml")
}

func isJSONContentType(contentType string) bool {
	lower := strings.ToLower(contentType)
	return strings.Contains(lower, "application/json") ||
		strings.Contains(lower, "text/json") ||
		strings.Contains(lower, "+json")
}

// extractTextFromHTML returns the visible text of a document, with the
// boilerplate elements dropped first.
func extractTextFromHTML(htmlContent string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(removeNoisyElements(htmlContent)))
	if err != nil {
		return "", err
	}
	return collapseWhitespace(doc.Find("body").Text()), nil
}

// extractHTMLBody returns the document's body markup, wrapped so it is a
// document again.
func extractHTMLBody(htmlContent string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return "", err
	}
	body, err := doc.Find("body").Html()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("no body content found in HTML")
	}
	return "<html>\n<body>\n" + body + "\n</body>\n</html>", nil
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// removeNoisyElements removes script, style, nav, header, footer, and other
// noisy elements from HTML to improve content extraction.
func removeNoisyElements(htmlContent string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		// If parsing fails, return original content.
		return htmlContent
	}

	// Elements to remove entirely.
	noisyTags := map[string]bool{
		"script":   true,
		"style":    true,
		"nav":      true,
		"header":   true,
		"footer":   true,
		"aside":    true,
		"noscript": true,
		"iframe":   true,
		"svg":      true,
	}

	var removeNodes func(*html.Node)
	removeNodes = func(n *html.Node) {
		var toRemove []*html.Node

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && noisyTags[c.Data] {
				toRemove = append(toRemove, c)
			} else {
				removeNodes(c)
			}
		}

		for _, node := range toRemove {
			n.RemoveChild(node)
		}
	}

	removeNodes(doc)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return htmlContent
	}

	return buf.String()
}

// cleanupMarkdown removes excessive whitespace and blank lines from markdown.
func cleanupMarkdown(content string) string {
	// Collapse multiple blank lines into at most two.
	content = multipleNewlinesRe.ReplaceAllString(content, "\n\n")

	// Remove trailing whitespace from each line.
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	content = strings.Join(lines, "\n")

	// Trim leading/trailing whitespace.
	content = strings.TrimSpace(content)

	return content
}

// ConvertHTMLToMarkdown converts HTML content to markdown format.
func ConvertHTMLToMarkdown(htmlContent string) (string, error) {
	converter := md.NewConverter("", true, nil)

	markdown, err := converter.ConvertString(htmlContent)
	if err != nil {
		return "", err
	}

	return markdown, nil
}

// FormatJSON formats JSON content with proper indentation.
func FormatJSON(content string) (string, error) {
	var data any
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// DownloadResult is one downloaded file.
type DownloadResult struct {
	// Path is where the file was written.
	Path string
	// Bytes is how much was written.
	Bytes int64
	// ContentType is the response's declared media type.
	ContentType string
	// NamedByServer reports that the name came from the response's
	// Content-Disposition header rather than from the caller or the URL.
	NamedByServer bool
}

// StreamURLToFile downloads a URL into dir without reading it into memory
// or requiring it to be text. The file is named by preferred when the
// caller supplied one, otherwise by the response's Content-Disposition
// header, otherwise by the URL.
func StreamURLToFile(ctx context.Context, client *http.Client, url, dir, preferred string) (DownloadResult, error) {
	if client == nil {
		client = DefaultHTTPClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", BrowserUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return DownloadResult{}, err
	}
	defer resp.Body.Close()

	result := DownloadResult{ContentType: resp.Header.Get("Content-Type")}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodySnippetLimit))
		return result, &HTTPStatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Snippet:    strings.TrimSpace(collapseWhitespace(string(snippet))),
			RetryAfter: resp.Header.Get("Retry-After"),
		}
	}

	// The server's own name for the file beats one guessed from the URL:
	// a download endpoint is often a path like /files/39281/download.
	name := sanitizeFileName(preferred)
	if name == "" {
		if fromHeader := contentDispositionFileName(resp.Header.Get("Content-Disposition")); fromHeader != "" {
			name = fromHeader
			result.NamedByServer = true
		}
	}
	if name == "" && resp.Request != nil && resp.Request.URL != nil {
		name = sanitizeFileName(fileNameFromURL(resp.Request.URL.String()))
	}
	if name == "" {
		name = "download"
	}
	result.Path = filepath.Join(dir, name)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return result, fmt.Errorf("failed to create parent directories: %w", err)
	}
	file, err := os.Create(result.Path)
	if err != nil {
		return result, fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	result.Bytes, err = io.Copy(file, resp.Body)
	if err != nil {
		return result, fmt.Errorf("failed to write file: %w", err)
	}
	return result, nil
}

// contentDispositionFileName reads the file name a server asked for.
// Both a plain filename and an RFC 5987 filename* are handled by mime,
// and a header that does not parse simply yields no name.
func contentDispositionFileName(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return sanitizeFileName(params["filename"])
}

// sanitizeFileName keeps only the base name, so neither a server nor a
// model can steer a download out of the directory it was given.
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return sanitizePathSegment(filepath.Base(filepath.FromSlash(name)))
}
