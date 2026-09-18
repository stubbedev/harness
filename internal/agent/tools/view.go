package tools

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
		return viewFileContent{}, failureResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
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
		return viewFileContent{response: &resp}, nil, nil
	}

	// Read the file content
	maxContentSize := MaxViewSize
	if isSkillFile {
		maxContentSize = 0
	}
	content, hasMore, err := readTextFile(filePath, params.Offset, params.Limit, maxContentSize)
	if err != nil {
		if tooLarge, ok := errors.AsType[contentTooLargeError](err); ok {
			return viewFileContent{}, failureResponse(fmt.Sprintf("Content section is too large (%d bytes). Maximum size is %d bytes",
				tooLarge.Size, tooLarge.Max)), nil
		}
		return viewFileContent{}, nil, fmt.Errorf("error reading file: %w", err)
	}
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

	if hasMore {
		output += fmt.Sprintf("\n\n(File has more lines. Use 'offset' parameter to read beyond line %d)",
			params.Offset+len(strings.Split(content, "\n")))
	}
	output += "\n</file>\n"
	output += reportDiagnosticsNow(ctx, v.lspManager, filePath)
	v.filetracker.RecordRead(ctx, sessionID, filePath)

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

	return viewFileContent{output: output, meta: meta}, nil, nil
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
func (v *viewTool) viewFiles(ctx context.Context, files []ViewFileRequest) (fantasy.ToolResponse, error) {
	sections := make([]string, 0, len(files))
	metas := make([]ViewResponseMetadata, 0, len(files))
	var firstFailure *fantasy.ToolResponse
	media, failed := 0, 0

	for _, f := range files {
		content, failure, err := v.viewOneFile(ctx, ViewParams{FilePath: f.FilePath, Offset: f.Offset, Limit: f.Limit})
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
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

func addLineNumbers(content string, startLine int) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")

	var result []string
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")

		lineNum := i + startLine
		numStr := fmt.Sprintf("%d", lineNum)

		if len(numStr) >= 6 {
			result = append(result, fmt.Sprintf("%s|%s", numStr, line))
		} else {
			paddedNum := fmt.Sprintf("%6s", numStr)
			result = append(result, fmt.Sprintf("%s|%s", paddedNum, line))
		}
	}

	return strings.Join(result, "\n")
}

func readTextFile(filePath string, offset, limit, maxContentSize int) (string, bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", false, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	skipped := 0
	for skipped < offset {
		_, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return "", false, nil
			}
			return "", false, err
		}
		skipped++
	}

	lines := make([]string, 0, min(limit, DefaultReadLimit))
	contentSize := 0

	for len(lines) < limit {
		lineText, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", false, err
		}
		lineText = strings.TrimSuffix(lineText, "\n")
		lineText = strings.TrimSuffix(lineText, "\r")
		if len(lineText) > MaxLineLength {
			// Truncate at a rune boundary to avoid splitting
			// multi-byte characters.
			lineText = strings.ToValidUTF8(lineText[:MaxLineLength], "") + "..."
		}
		projectedSize := contentSize + len(lineText)
		if len(lines) > 0 {
			projectedSize++
		}
		if maxContentSize > 0 && projectedSize > maxContentSize {
			return "", false, contentTooLargeError{Size: projectedSize, Max: maxContentSize}
		}
		contentSize = projectedSize
		lines = append(lines, lineText)
		if err == io.EOF {
			break
		}
	}

	// Peek one more line only when we filled the limit.
	hasMore := false
	if len(lines) == limit {
		lineText, peekErr := reader.ReadString('\n')
		hasMore = len(lineText) > 0 || peekErr == nil
	}

	return strings.Join(lines, "\n"), hasMore, nil
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
