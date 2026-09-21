package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/JohannesKaufmann/html-to-markdown/plugin"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
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
func FetchURL(ctx context.Context, client *http.Client, rawURL string, format FetchFormat) (FetchResult, error) {
	if client == nil {
		client = DefaultHTTPClient()
	}
	rawURL = rewriteGitHubBlobURL(rawURL)

	resp, err := doFetchRequest(ctx, client, rawURL)
	if err != nil {
		return FetchResult{}, err
	}
	defer resp.Body.Close()

	result := FetchResult{
		ContentType: resp.Header.Get("Content-Type"),
		FinalURL:    rawURL,
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

	result.ContentType = sniffContentType(result.ContentType, body)
	content := string(decodeTextBody(body, result.ContentType))
	if !utf8.ValidString(content) {
		// A PDF or an image is not an error: say what it is and let the
		// model decide to download it instead.
		result.Binary = true
		return result, nil
	}

	result.Content, err = convertFetchedContent(content, result.ContentType, result.FinalURL, format)
	if err != nil {
		return result, err
	}
	return result, nil
}

// newFetchRequest builds a page request that looks like a browser's.
func newFetchRequest(ctx context.Context, rawURL, userAgent, referer string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	return req, nil
}

const (
	// fetchRetryDefaultDelay is the pause before the one retry when the
	// server did not say how long to wait.
	fetchRetryDefaultDelay = time.Second
	// fetchRetryMaxDelay caps a Retry-After: a tool call cannot sit on a
	// server's hour-long request.
	fetchRetryMaxDelay = 10 * time.Second
)

// doFetchRequest performs the request and, on the statuses a bot filter
// or a rate limiter answers with, tries once more as a different browser
// arriving from the site's own origin. The caller closes the body.
func doFetchRequest(ctx context.Context, client *http.Client, rawURL string) (*http.Response, error) {
	req, err := newFetchRequest(ctx, rawURL, BrowserUserAgent, "")
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if !isRetryableFetchStatus(resp.StatusCode) {
		return resp, nil
	}

	delay := fetchRetryDelay(resp.Header.Get("Retry-After"))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, errorBodySnippetLimit))
	_ = resp.Body.Close()

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}

	retry, err := newFetchRequest(ctx, rawURL, userAgents[rand.IntN(len(userAgents))], originOf(rawURL))
	if err != nil {
		return nil, err
	}
	return client.Do(retry)
}

func isRetryableFetchStatus(status int) bool {
	switch status {
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

// fetchRetryDelay reads a Retry-After header, in either of its forms,
// into a bounded pause.
func fetchRetryDelay(retryAfter string) time.Duration {
	retryAfter = strings.TrimSpace(retryAfter)
	if retryAfter == "" {
		return fetchRetryDefaultDelay
	}
	var delay time.Duration
	if seconds, err := strconv.Atoi(retryAfter); err == nil {
		delay = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(retryAfter); err == nil {
		delay = time.Until(at)
	} else {
		return fetchRetryDefaultDelay
	}
	return min(max(delay, 0), fetchRetryMaxDelay)
}

// originOf is the scheme and host of a URL as a Referer value, or empty
// when the URL does not parse.
func originOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/"
}

// rewriteGitHubBlobURL turns a GitHub file page into its raw counterpart,
// so the model reads the file rather than the web UI wrapped around it.
// Anything that is not a blob URL comes back unchanged.
func rewriteGitHubBlobURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Host != "github.com" && u.Host != "www.github.com") {
		return rawURL
	}
	// owner / repo / "blob" / ref / path, with the path keeping its slashes.
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 5)
	if len(parts) < 5 || parts[2] != "blob" || parts[0] == "" || parts[1] == "" || parts[3] == "" || parts[4] == "" {
		return rawURL
	}
	return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/" + parts[3] + "/" + parts[4]
}

// sniffContentType corrects a header that says nothing useful about a
// body that is plainly HTML: a missing type, text/plain or octet-stream
// over a document starting with a doctype or an html tag becomes
// text/html, keeping any charset the header did declare.
func sniffContentType(header string, body []byte) string {
	mediaType := strings.ToLower(strings.TrimSpace(header))
	var params map[string]string
	if parsed, p, err := mime.ParseMediaType(header); err == nil {
		mediaType, params = parsed, p
	}
	switch mediaType {
	case "", "text/plain", "application/octet-stream":
	default:
		return header
	}
	if !looksLikeHTML(body) {
		return header
	}
	return mime.FormatMediaType("text/html", params)
}

// htmlSniffLimit is how far into a body the HTML sniff looks.
const htmlSniffLimit = 512

func looksLikeHTML(body []byte) bool {
	head := body[:min(len(body), htmlSniffLimit)]
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	head = bytes.ToLower(bytes.TrimLeft(head, " \t\r\n\f"))
	for _, marker := range []string{"<!doctype html", "<html", "<head", "<body"} {
		if bytes.HasPrefix(head, []byte(marker)) {
			return true
		}
	}
	return false
}

// decodeTextBody converts a text response to UTF-8 from the charset its
// header, BOM or meta tag declares. A page that declares nothing and is
// not UTF-8 is read as Windows-1252, the web's historical default, rather
// than reported as binary. Non-text types pass through untouched.
func decodeTextBody(body []byte, contentType string) []byte {
	if len(body) == 0 || !isTextContentType(contentType) {
		return body
	}
	enc, name, certain := charset.DetermineEncoding(body, contentType)
	if name == "utf-8" && !certain && !utf8.Valid(body) {
		// The UTF-8 guess is made on the first kilobyte; when it does not
		// hold for the whole body, nothing declared a charset at all.
		enc, _ = charset.Lookup("windows-1252")
	}
	if enc == nil {
		return body
	}
	decoded, err := enc.NewDecoder().Bytes(body)
	if err != nil {
		return body
	}
	return decoded
}

// isTextContentType reports a media type worth charset decoding.
func isTextContentType(contentType string) bool {
	lower := strings.ToLower(contentType)
	return strings.HasPrefix(lower, "text/") ||
		isHTMLContentType(lower) ||
		isJSONContentType(lower) ||
		strings.Contains(lower, "xml") ||
		strings.Contains(lower, "javascript") ||
		strings.Contains(lower, "ecmascript")
}

// convertFetchedContent renders a text response in the requested format.
// Non-HTML content is returned as-is, except JSON, which is indented. The
// base URL is what relative links in an HTML page resolve against.
func convertFetchedContent(content, contentType, baseURL string, format FetchFormat) (string, error) {
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
			markdown, err := ConvertHTMLToMarkdown(removeNoisyElements(content), baseURL)
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
// noisy elements from HTML to improve content extraction. A header inside
// main or article is the article's own heading, not site chrome, so it
// stays.
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

	var removeNodes func(n *html.Node, inContent bool)
	removeNodes = func(n *html.Node, inContent bool) {
		var toRemove []*html.Node

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			isElement := c.Type == html.ElementNode
			keepHeader := inContent && c.Data == "header"
			if isElement && noisyTags[c.Data] && !keepHeader {
				toRemove = append(toRemove, c)
			} else {
				removeNodes(c, inContent || (isElement && (c.Data == "main" || c.Data == "article")))
			}
		}

		for _, node := range toRemove {
			n.RemoveChild(node)
		}
	}

	removeNodes(doc, false)

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

// ConvertHTMLToMarkdown converts HTML content to GitHub-flavored markdown,
// so tables, strikethrough and fenced code survive. Links and images are
// made absolute against baseURL (the page's final URL, or its <base>),
// and the conversion starts at the page's main content when it has one.
func ConvertHTMLToMarkdown(htmlContent, baseURL string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return "", err
	}

	var domain string
	if base := documentBaseURL(doc, baseURL); base != nil {
		domain = base.Host
		absolutizeURLs(doc, base)
	}

	converter := md.NewConverter(domain, true, &md.Options{CodeBlockStyle: "fenced"})
	converter.Use(plugin.GitHubFlavored())
	return converter.Convert(mainContentRoot(doc)), nil
}

// documentBaseURL is what relative URLs in the document resolve against:
// its <base href> when it has one, otherwise the URL it was fetched from.
// It is nil when neither parses.
func documentBaseURL(doc *goquery.Document, pageURL string) *url.URL {
	base, err := url.Parse(pageURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		base = nil
	}
	if href, ok := doc.Find("base[href]").First().Attr("href"); ok {
		if ref, err := url.Parse(strings.TrimSpace(href)); err == nil {
			if base != nil {
				ref = base.ResolveReference(ref)
			}
			if ref.Scheme != "" && ref.Host != "" {
				base = ref
			}
		}
	}
	return base
}

// absolutizeURLs resolves every link and image against base, so the
// markdown carries URLs the model can fetch rather than paths relative to
// a page it no longer has.
func absolutizeURLs(doc *goquery.Document, base *url.URL) {
	for _, target := range []struct{ selector, attr string }{
		{"a[href]", "href"},
		{"img[src]", "src"},
	} {
		doc.Find(target.selector).Each(func(_ int, s *goquery.Selection) {
			raw, _ := s.Attr(target.attr)
			ref, err := url.Parse(strings.TrimSpace(raw))
			if err != nil {
				return
			}
			s.SetAttr(target.attr, base.ResolveReference(ref).String())
		})
	}
}

// mainContentMinPercent is how much of the body's text a main, article
// or role=main element must hold to be converted on its own. Below it
// the element is a teaser or a sidebar, and the body stays the root.
const mainContentMinPercent = 40

// mainContentRoot picks the element the conversion starts from: the
// page's main content when it is marked up and substantial, otherwise
// the whole document.
func mainContentRoot(doc *goquery.Document) *goquery.Selection {
	bodyLen := len(collapseWhitespace(doc.Find("body").Text()))
	if bodyLen == 0 {
		return doc.Selection
	}
	for _, selector := range []string{"main", "article", "[role=main]"} {
		node := doc.Find(selector).First()
		if node.Length() == 0 {
			continue
		}
		if len(collapseWhitespace(node.Text()))*100 >= bodyLen*mainContentMinPercent {
			return node
		}
	}
	return doc.Selection
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
