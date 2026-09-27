package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"hash"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/filepathext"
	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/skills"
)

//go:embed view.md.tpl
var viewDescriptionTmpl []byte

var viewDescriptionTpl = template.Must(
	template.New("viewDescription").
		Parse(string(viewDescriptionTmpl)),
)

type viewDescriptionData struct {
	DefaultReadLimit int
	MaxViewSizeKB    int
	MaxFilesPerCall  int
}

func viewDescription() string {
	return renderTemplate(viewDescriptionTpl, viewDescriptionData{
		DefaultReadLimit: DefaultReadLimit,
		MaxViewSizeKB:    MaxViewSize / 1024,
		MaxFilesPerCall:  MaxViewFilesPerCall,
	})
}

// ViewFileRequest is one file (or one section of one file) inside a
// multi-file view call. It mirrors the single-file parameters.
type ViewFileRequest struct {
	FilePath string `json:"file_path" description:"The path to the file to read"`
	Offset   int    `json:"offset,omitempty" description:"The line number to start reading from (0-based)"`
	Limit    int    `json:"limit,omitempty" description:"The number of lines to read (defaults to 200)"`
}

type ViewParams struct {
	FilePath string `json:"file_path,omitempty" description:"The path to the file to read"`
	Offset   int    `json:"offset,omitempty" description:"The line number to start reading from (0-based)"`
	Limit    int    `json:"limit,omitempty" description:"The number of lines to read (defaults to 200)"`
	// Files reads several files, or several sections of different files,
	// in one call. Set file_path or files, never both.
	Files []ViewFileRequest `json:"files,omitempty" description:"Read several files in one call; each entry takes file_path, offset and limit"`
}

type ViewResourceType string

const (
	ViewResourceUnset ViewResourceType = ""
	ViewResourceSkill ViewResourceType = "skill"
)

type ViewResponseMetadata struct {
	FilePath            string           `json:"file_path"`
	Content             string           `json:"content"`
	ResourceType        ViewResourceType `json:"resource_type,omitempty"`
	ResourceName        string           `json:"resource_name,omitempty"`
	ResourceDescription string           `json:"resource_description,omitempty"`
}

// ViewFilesResponseMetadata is the response metadata of a multi-file
// view: one entry per file that was read successfully.
type ViewFilesResponseMetadata struct {
	Files []ViewResponseMetadata `json:"files"`
}

const (
	ViewToolName     = "view"
	MaxViewSize      = 200 * 1024 // 200KB
	DefaultReadLimit = 200
	MaxLineLength    = 2000
	// MaxViewFilesPerCall bounds one multi-file call so a single wide
	// read cannot flood the context; more files just take another call.
	MaxViewFilesPerCall = 10
)

type contentTooLargeError struct {
	Size int
	Max  int
}

func (e contentTooLargeError) Error() string {
	return fmt.Sprintf("content section is too large (%d bytes). Maximum size is %d bytes", e.Size, e.Max)
}

// viewTool carries the view tool's dependencies; one method per call
// shape, all sharing viewOneFile so the two shapes cannot drift.
type viewTool struct {
	lspManager   *lsp.Manager
	filetracker  filetracker.Service
	skillTracker *skills.Tracker
	workingDir   string
	skillsPaths  []string
}

// viewFileContent is the outcome of reading one file. Text reads land
// in output (the wrapped section the model sees) and meta; media reads
// return their response so it can be returned to the model as-is.
type viewFileContent struct {
	output   string
	meta     ViewResponseMetadata
	response *fantasy.ToolResponse
	observe  func()
}

// viewOneFile reads one file or one section of it. A refusal (missing
// file, oversized section, directory) comes back as a failure response
// instead of an error, so a multi-file call can report it per file and
// a single-file call returns it directly. err is reserved for hard
// failures that have nothing to do with the file itself.
func (v *viewTool) viewOneFile(ctx context.Context, params ViewParams) (viewFileContent, *fantasy.ToolResponse, error) {
	if params.FilePath == "" {
		return viewFileContent{}, failureResponse("file_path is required"), nil
	}

	// Handle builtin skill files (harness: prefix).
	if strings.HasPrefix(params.FilePath, skills.BuiltinPrefix) {
		return v.readBuiltinFile(params)
	}

	// Handle relative paths
	filePath := filepathext.SmartJoin(v.workingDir, params.FilePath)

	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return viewFileContent{}, nil, fmt.Errorf("error resolving file path: %w", err)
	}

	isSkillFile := isInSkillsPath(absFilePath, v.skillsPaths)

	sessionID, err := SessionIDOrError(ctx, "reading files")
	if err != nil {
		return viewFileContent{}, nil, err
	}

	// Check if file exists
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to offer suggestions for similarly named files
			dir := filepath.Dir(filePath)
			base := filepath.Base(filePath)

			dirEntries, dirErr := os.ReadDir(dir)
			if dirErr == nil {
				var suggestions []string
				for _, entry := range dirEntries {
					if strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(base)) ||
						strings.Contains(strings.ToLower(base), strings.ToLower(entry.Name())) {
						suggestions = append(suggestions, filepath.Join(dir, entry.Name()))
						if len(suggestions) >= 3 {
							break
						}
					}
				}

				if len(suggestions) > 0 {
					return viewFileContent{}, failureResponse(fmt.Sprintf("File not found: %s\n\nDid you mean one of these?\n%s",
						filePath, strings.Join(suggestions, "\n"))), nil
				}
			}

			return viewFileContent{}, failureResponse(fmt.Sprintf("File not found: %s", filePath)), nil
		}
		return viewFileContent{}, nil, fmt.Errorf("error accessing file: %w", err)
	}

	// Check if it's a directory
	if fileInfo.IsDir() {
		return viewDirectory(filePath, params.Offset, params.Limit)
	}

	// Set default limit if not provided (no limit for SKILL.md files)
	if params.Limit <= 0 {
		if isSkillFile {
			params.Limit = 1000000 // Effectively no limit for skill files
		} else {
			params.Limit = DefaultReadLimit
		}
	}

	isSupportedImage, mimeType := getImageMimeType(filePath)
	if isSupportedImage {
		if fileInfo.Size() > MaxViewSize {
			return viewFileContent{}, failureResponse(fmt.Sprintf("Image file is too large (%d bytes). Maximum size is %d bytes",
				fileInfo.Size(), MaxViewSize)), nil
		}
		if !GetSupportsImagesFromContext(ctx) {
			modelName := GetModelNameFromContext(ctx)
			return viewFileContent{}, failureResponse(fmt.Sprintf("This model (%s) does not support image data.", modelName)), nil
		}

		imageData, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return viewFileContent{}, nil, fmt.Errorf("error reading image file: %w", readErr)
		}

		// Some tools save files with a mismatched extension
		// (e.g. pinchtab writes JPEG bytes to a .png file).
		// Providers like Anthropic strictly validate the
		// media type against the base64 magic bytes and 400
		// on mismatch, so prefer the sniffed type whenever
		// it identifies a supported image format.
		mimeType = sniffImageMimeType(imageData, mimeType)

		resp := fantasy.NewImageResponse(imageData, mimeType)
		return viewFileContent{response: &resp, observe: func() {
			filetracker.Observe(ctx, v.filetracker, sessionID, filePath, imageData, []filetracker.Range{{Start: 0, End: len(imageData)}})
		}}, nil, nil
	}

	// Read the file content
	maxContentSize := MaxViewSize
	if isSkillFile {
		maxContentSize = 0
	}
	read, err := readTextForView(filePath, params.Offset, params.Limit, maxContentSize, v.filetracker)
	if err != nil {
		if tooLarge, ok := errors.AsType[contentTooLargeError](err); ok {
			return viewFileContent{}, failureResponse(fmt.Sprintf("Content section is too large (%d bytes). Maximum size is %d bytes",
				tooLarge.Size, tooLarge.Max)), nil
		}
		return viewFileContent{}, nil, fmt.Errorf("error reading file: %w", err)
	}
	content := read.content
	if !utf8.ValidString(content) {
		return viewFileContent{}, failureResponse("File content is not valid UTF-8"), nil
	}

	// Reading a file never starts a language server it has to wait
	// for. Whatever the servers have already published about this
	// file is reported below; anything they are still working out
	// shows up in a later report.
	openInLSPs(ctx, v.lspManager, filePath)
	output := fmt.Sprintf("<file path=%q>\n", filePath)
	output += addLineNumbers(content, params.Offset+1)

	if read.hasMore {
		output += fmt.Sprintf("\n\n(File has more lines. Use 'offset' parameter to read beyond line %d)",
			params.Offset+max(read.lines, 1))
	}
	output += "\n</file>\n"
	output += reportDiagnosticsNow(ctx, v.lspManager, filePath)

	meta := ViewResponseMetadata{
		FilePath: filePath,
		Content:  content,
	}
	if isSkillFile {
		if skill, err := skills.Parse(filePath); err == nil {
			meta.ResourceType = ViewResourceSkill
			meta.ResourceName = skill.Name
			meta.ResourceDescription = skill.Description
			v.skillTracker.MarkLoaded(skill.Name)
		}
	}

	return viewFileContent{output: output, meta: meta, observe: func() {
		v.observeTextRead(ctx, sessionID, filePath, read)
	}}, nil, nil
}

// run serves one view call. The single-file form returns the read (or
// its failure) directly; the multi-file form reads each file through
// the same path and reports per-file failures as sections, so one bad
// path does not waste the other reads in the call.
func (v *viewTool) run(ctx context.Context, params ViewParams) (fantasy.ToolResponse, error) {
	switch {
	case len(params.Files) == 0:
		if params.FilePath == "" {
			return fantasy.NewTextErrorResponse("file_path is required"), nil
		}
		content, failure, err := v.viewOneFile(ctx, params)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		if failure != nil {
			return *failure, nil
		}
		if content.observe != nil {
			content.observe()
		}
		if content.response != nil {
			return *content.response, nil
		}
		return fantasy.WithResponseMetadata(
			fantasy.NewTextResponse(content.output),
			content.meta,
		), nil
	case params.FilePath != "":
		return fantasy.NewTextErrorResponse("Pass either file_path or files, not both"), nil
	case len(params.Files) > MaxViewFilesPerCall:
		return fantasy.NewTextErrorResponse(fmt.Sprintf("At most %d files per call, got %d; read the rest in a follow-up call",
			MaxViewFilesPerCall, len(params.Files))), nil
	}
	return v.viewFiles(ctx, params.Files)
}

// viewFiles reads every requested file and concatenates the sections.
// A file that cannot be read becomes an error section naming the path;
// only when every entry failed does the call itself fail.
//
// The reads run concurrently: each is an independent stat-open-scan, so
// ten files cost the slowest read rather than the sum of them. Sections
// and metadata are assembled in request order afterwards.
func (v *viewTool) viewFiles(ctx context.Context, files []ViewFileRequest) (fantasy.ToolResponse, error) {
	contents := make([]viewFileContent, len(files))
	failures := make([]*fantasy.ToolResponse, len(files))
	hardErrs := make([]error, len(files))

	var wg sync.WaitGroup
	for i, f := range files {
		wg.Go(func() {
			contents[i], failures[i], hardErrs[i] = v.viewOneFile(ctx, ViewParams{
				FilePath: f.FilePath,
				Offset:   f.Offset,
				Limit:    f.Limit,
			})
		})
	}
	wg.Wait()

	// A hard failure (a failed read error, a cancelled context) aborts the
	// call, as the sequential version did at the first one it hit.
	for _, err := range hardErrs {
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
	}

	sections := make([]string, 0, len(files))
	metas := make([]ViewResponseMetadata, 0, len(files))
	var firstFailure *fantasy.ToolResponse
	media, failed := 0, 0

	for i, f := range files {
		content, failure := contents[i], failures[i]
		path := f.FilePath
		if path == "" {
			path = "(missing file_path)"
		}
		switch {
		case failure != nil:
			failed++
			if firstFailure == nil {
				firstFailure = failure
			}
			sections = append(sections, fmt.Sprintf("<file path=%q error>\n%s\n</file>\n", path, failure.Content))
		case content.response != nil:
			// Media cannot be embedded in a text section; the model
			// views it on its own.
			failed++
			media++
			sections = append(sections, fmt.Sprintf("<file path=%q error>\n%s is a media file; view it on its own with file_path.\n</file>\n", path, path))
		default:
			if content.observe != nil {
				content.observe()
			}
			sections = append(sections, content.output)
			metas = append(metas, content.meta)
		}
	}

	if failed == len(files) {
		if firstFailure != nil && media == 0 {
			return *firstFailure, nil
		}
		return fantasy.NewTextErrorResponse("None of the entries could be read as text; view media files one at a time with file_path"), nil
	}

	resp := fantasy.NewTextResponse(strings.Join(sections, "\n"))
	if len(metas) > 0 {
		resp = fantasy.WithResponseMetadata(resp, ViewFilesResponseMetadata{Files: metas})
	}
	return resp, nil
}

func NewViewTool(
	lspManager *lsp.Manager,
	filetracker filetracker.Service,
	skillTracker *skills.Tracker,
	workingDir string,
	skillsPaths ...string,
) fantasy.AgentTool {
	v := &viewTool{
		lspManager:   lspManager,
		filetracker:  filetracker,
		skillTracker: skillTracker,
		workingDir:   workingDir,
		skillsPaths:  skillsPaths,
	}
	return fantasy.NewParallelAgentTool(
		ViewToolName,
		viewDescription(),
		func(ctx context.Context, params ViewParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return v.run(ctx, params)
		},
	)
}

func failureResponse(message string) *fantasy.ToolResponse {
	resp := fantasy.NewTextErrorResponse(message)
	return &resp
}

// addLineNumbers prefixes each line with its line number and a pipe. No
// padding: the model does not need the columns to line up, and the
// blanks were about two tokens per line on every file read.
func addLineNumbers(content string, startLine int) string {
	if content == "" {
		return ""
	}

	var result strings.Builder
	lineNum := startLine
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSuffix(line, "\r")

		result.WriteString(strconv.Itoa(lineNum))
		result.WriteByte('|')
		result.WriteString(line)
		result.WriteByte('\n')
		lineNum++
	}

	return strings.TrimSuffix(result.String(), "\n")
}

// textView is the outcome of one read of a file: the requested
// section, whether lines follow it, the sha256 of the file's full
// contents — the version the edit guards compare against — and the
// byte ranges the section covers verbatim. data is set only by the
// whole-file fallback, which keeps the bytes it read.
type textView struct {
	content string
	lines   int
	hasMore bool
	version [sha256.Size]byte
	ranges  []filetracker.Range
	data    []byte
}

// versionObserver is the filetracker capability to register a content
// version the caller hashed while reading the file, so a streamed view
// never holds the bytes just to have them hashed. Trackers without it
// observe whole-file bytes instead.
type versionObserver interface {
	ObserveVersion(ctx context.Context, session, path string, version [sha256.Size]byte, ranges []filetracker.Range)
}

// readTextForView reads one section of filePath for a view. The normal
// path streams the file and hashes it as it goes; the whole bytes are
// only materialised when the evidence registry needs them — a negative
// offset, whose returned lines pair against clamped positions, or an
// evidence tracker that cannot take a precomputed version.
func readTextForView(filePath string, offset, limit, maxContentSize int, tracker filetracker.Service) (textView, error) {
	if offset < 0 || needsWholeFileRead(tracker) {
		return readTextWhole(filePath, offset, limit, maxContentSize)
	}
	return streamTextFile(filePath, offset, limit, maxContentSize)
}

// needsWholeFileRead reports whether a tracker can only register
// evidence from whole-file bytes. Evidence trackers observe a version
// hash, but only versionObserver trackers accept one that was computed
// outside their own read.
func needsWholeFileRead(tracker filetracker.Service) bool {
	if _, ok := tracker.(versionObserver); ok {
		return false
	}
	_, isEvidence := tracker.(filetracker.Evidence)
	return isEvidence
}

// observeTextRead registers a text view with the file tracker. A
// streamed read carries the version hash it computed along the way, so
// observers that accept one never see the file bytes; the whole-file
// fallback observes the bytes it read.
func (v *viewTool) observeTextRead(ctx context.Context, sessionID, filePath string, read textView) {
	if read.data != nil {
		filetracker.Observe(ctx, v.filetracker, sessionID, filePath, read.data, read.ranges)
		return
	}
	if observer, ok := v.filetracker.(versionObserver); ok {
		observer.ObserveVersion(ctx, sessionID, filePath, read.version, read.ranges)
		return
	}
	// Content is only hashed by evidence trackers; plain trackers
	// record the read alone.
	filetracker.Observe(ctx, v.filetracker, sessionID, filePath, nil, read.ranges)
}

func readTextFile(filePath string, offset, limit, maxContentSize int) (string, bool, error) {
	read, err := streamTextFile(filePath, offset, limit, maxContentSize)
	return read.content, read.hasMore, err
}

// streamTextFile reads filePath in one buffered pass, retaining only
// the requested section. The buffer scales with the file, capped, so a
// deep section costs a handful of read calls instead of one per
// bufio-sized chunk of the file.
func streamTextFile(filePath string, offset, limit, maxContentSize int) (textView, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return textView{}, err
	}
	defer file.Close()

	bufferSize := 4096
	if info, err := file.Stat(); err == nil && info.Size() > int64(bufferSize) {
		bufferSize = int(min(info.Size(), 32*1024))
	}
	return streamText(bufio.NewReaderSize(file, bufferSize), offset, limit, maxContentSize)
}

// readTextWhole reads a section the way views did before streaming:
// whole file into memory, section cut from the bytes, ranges located
// in those bytes. It is the fallback for reads whose evidence pairing
// needs bytes the stream does not keep.
func readTextWhole(filePath string, offset, limit, maxContentSize int) (textView, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return textView{}, err
	}
	read, err := streamText(bufio.NewReader(bytes.NewReader(data)), offset, limit, maxContentSize)
	if err != nil {
		return textView{}, err
	}
	read.data = data
	read.version = sha256.Sum256(data)
	read.ranges = seenTextRanges(data, offset, read.content)
	return read, nil
}

// stagedHash feeds a sha256 stream through a staging buffer. The
// version hash has to cover bytes arriving line by line, and the block
// cipher underneath amortizes only over contiguous runs, so forty
// bytes at a time it spends all its time on call overhead.
type stagedHash struct {
	stage []byte
	hash  hash.Hash
}

func newStagedHash() *stagedHash {
	return &stagedHash{
		stage: make([]byte, 0, 32*1024),
		hash:  sha256.New(),
	}
}

// write adds p to the stream: flushing the stage first when p would
// overflow it, and passing p straight through when it alone overflows
// a flushed stage. The order of the bytes is never shuffled.
func (s *stagedHash) write(p []byte) {
	if len(s.stage)+len(p) > cap(s.stage) {
		s.flush()
	}
	if len(p) > cap(s.stage) {
		s.hash.Write(p)
		return
	}
	s.stage = append(s.stage, p...)
}

func (s *stagedHash) flush() {
	if len(s.stage) > 0 {
		s.hash.Write(s.stage)
		s.stage = s.stage[:0]
	}
}

// sum flushes the stage and returns the stream's hash.
func (s *stagedHash) sum() [sha256.Size]byte {
	s.flush()
	var version [sha256.Size]byte
	s.hash.Sum(version[:0])
	return version
}

// streamText reads one section of a file in a single buffered pass.
// Only the section is retained; skipped and trailing lines stream past
// a fixed buffer so the version hash can cover the whole file without
// holding it. Viewing 100 lines of a 50k-line file allocates for 100
// lines, not 50k.
//
// ranges pairs each returned line with the file bytes it came from,
// located the way seenTextRanges locates them in a whole-file
// snapshot: the whole physical line, newline included, or the
// truncated prefix of an overlong line without it. It is exact for a
// non-negative offset and nil for a negative one, whose clamped
// pairing readTextWhole reproduces from whole-file bytes.
func streamText(source *bufio.Reader, offset, limit, maxContentSize int) (textView, error) {
	hash := newStagedHash()
	pos := 0
	scratch := make([]byte, 0, 4096)

	// readLine returns the next physical line, newline included, in
	// scratch — valid only until the next call. Every byte read is
	// hashed on its way past, so the version covers the file even
	// where nothing is retained. An overlong line is drained with
	// further ReadSlice calls until its newline or the end of the
	// file turns up.
	readLine := func() ([]byte, error) {
		scratch = scratch[:0]
		for {
			chunk, err := source.ReadSlice('\n')
			pos += len(chunk)
			hash.write(chunk)
			scratch = append(scratch, chunk...)
			if err == bufio.ErrBufferFull {
				continue
			}
			return scratch, err
		}
	}

	// Skip to the section start. EOF here means the offset is past
	// the end of the file, so the section is empty and the pairing
	// anchor sits at the end of the file.
	for range max(0, offset) {
		if _, err := readLine(); err != nil {
			if err != io.EOF {
				return textView{}, err
			}
			return textView{version: hash.sum(), ranges: []filetracker.Range{{Start: pos, End: pos}}}, nil
		}
	}

	var content []byte
	contentSize := 0
	lines := 0
	var ranges []filetracker.Range

	for lines < limit {
		lineStart := pos
		line, err := readLine()
		if err != nil && err != io.EOF {
			return textView{}, err
		}
		text := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		truncated := false
		var prefix []byte
		if len(text) > MaxLineLength {
			// Truncate at a rune boundary to avoid splitting
			// multi-byte characters.
			truncated = true
			prefix = bytes.ToValidUTF8(text[:MaxLineLength], nil)
			text = append(slices.Clip(prefix), "..."...)
		}
		projectedSize := contentSize + len(text)
		if lines > 0 {
			projectedSize++
		}
		if maxContentSize > 0 && projectedSize > maxContentSize {
			return textView{}, contentTooLargeError{Size: projectedSize, Max: maxContentSize}
		}
		contentSize = projectedSize
		if lines > 0 {
			content = append(content, '\n')
		}
		content = append(content, text...)
		lines++

		if offset >= 0 {
			r := filetracker.Range{Start: lineStart, End: pos}
			if truncated {
				r.End = lineStart + len(prefix)
			}
			ranges = append(ranges, r)
		}
		if err == io.EOF {
			break
		}
	}

	// Peek one more line only when we filled the limit.
	hasMore := false
	if lines == limit {
		peeked, err := readLine()
		hasMore = len(peeked) > 0 || err == nil
	}

	// The rest of the file streams past the hash: the version has
	// to cover bytes the section never touched.
	for {
		if _, err := readLine(); err != nil {
			if err == io.EOF {
				break
			}
			return textView{}, err
		}
	}

	version := hash.sum()
	return textView{
		content: string(content),
		lines:   lines,
		hasMore: hasMore,
		version: version,
		ranges:  ranges,
	}, nil
}

func getImageMimeType(filePath string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		return true, "image/jpeg"
	case ".png":
		return true, "image/png"
	case ".gif":
		return true, "image/gif"
	case ".webp":
		return true, "image/webp"
	default:
		return false, ""
	}
}

// sniffImageMimeType returns the content-sniffed MIME type when it identifies
// a supported image format. Otherwise it returns the provided fallback, which
// is usually the extension-derived type. Providers that validate the image
// media type against the base64 magic bytes (e.g. Anthropic) reject mismatched
// requests with a 400, so trusting the filename alone is unsafe.
func sniffImageMimeType(data []byte, fallback string) string {
	sniffed := http.DetectContentType(data)
	// http.DetectContentType may return the MIME with a ";" parameter
	// (e.g. "image/svg+xml; charset=utf-8") although current image sniffers
	// return bare types; strip defensively.
	if i := strings.IndexByte(sniffed, ';'); i >= 0 {
		sniffed = strings.TrimSpace(sniffed[:i])
	}
	switch sniffed {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return sniffed
	}
	return fallback
}

// isInSkillsPath checks if filePath is within any of the configured skills
// directories. Returns true for files that can be read without permission
// prompts and without size limits.
//
// Note that symlinks are resolved to prevent path traversal attacks via
// symbolic links.
func isInSkillsPath(filePath string, skillsPaths []string) bool {
	if len(skillsPaths) == 0 {
		return false
	}

	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}

	evalFilePath, err := filepath.EvalSymlinks(absFilePath)
	if err != nil {
		return false
	}

	for _, skillsPath := range skillsPaths {
		absSkillsPath, err := filepath.Abs(skillsPath)
		if err != nil {
			continue
		}

		evalSkillsPath, err := filepath.EvalSymlinks(absSkillsPath)
		if err != nil {
			continue
		}

		relPath, err := filepath.Rel(evalSkillsPath, evalFilePath)
		if err == nil && !strings.HasPrefix(relPath, "..") {
			return true
		}
	}

	return false
}

// readBuiltinFile reads a file from the embedded builtin skills filesystem.
func (v *viewTool) readBuiltinFile(params ViewParams) (viewFileContent, *fantasy.ToolResponse, error) {
	embeddedPath := "builtin/" + strings.TrimPrefix(params.FilePath, skills.BuiltinPrefix)
	builtinFS := skills.BuiltinFS()

	data, err := fs.ReadFile(builtinFS, embeddedPath)
	if err != nil {
		return viewFileContent{}, failureResponse(fmt.Sprintf("Builtin file not found: %s", params.FilePath)), nil
	}

	content := string(data)
	if !utf8.ValidString(content) {
		return viewFileContent{}, failureResponse("File content is not valid UTF-8"), nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 1000000 // Effectively no limit for skill files.
	}

	lines := strings.Split(content, "\n")
	offset := min(params.Offset, len(lines))
	lines = lines[offset:]

	hasMore := len(lines) > limit
	if hasMore {
		lines = lines[:limit]
	}

	output := fmt.Sprintf("<file path=%q>\n", params.FilePath)
	output += addLineNumbers(strings.Join(lines, "\n"), offset+1)
	if hasMore {
		output += fmt.Sprintf("\n\n(File has more lines. Use 'offset' parameter to read beyond line %d)",
			offset+len(lines))
	}
	output += "\n</file>\n"

	meta := ViewResponseMetadata{
		FilePath: params.FilePath,
		Content:  strings.Join(lines, "\n"),
	}
	if skill, err := skills.ParseContent(data); err == nil {
		meta.ResourceType = ViewResourceSkill
		meta.ResourceName = skill.Name
		meta.ResourceDescription = skill.Description
		v.skillTracker.MarkLoaded(skill.Name)
	}

	return viewFileContent{output: output, meta: meta}, nil, nil
}
