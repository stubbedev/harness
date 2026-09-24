package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/diff"
	"github.com/stubbedev/harness/internal/filepathext"
	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
)

type EditOperation struct {
	OldString  string `json:"old_string" description:"The text to replace"`
	NewString  string `json:"new_string" description:"The text to replace it with"`
	ReplaceAll bool   `json:"replace_all,omitempty" description:"Replace all occurrences of old_string (default false)."`
}

type EditParams struct {
	FilePath string          `json:"file_path" description:"The absolute path to the file to modify"`
	Edits    []EditOperation `json:"edits" description:"Array of edit operations to perform sequentially on the file"`
}

type FailedEdit struct {
	Index int           `json:"index"`
	Error string        `json:"error"`
	Edit  EditOperation `json:"edit"`
}

type EditResponseMetadata struct {
	Additions    int          `json:"additions"`
	Removals     int          `json:"removals"`
	OldContent   string       `json:"old_content,omitempty"`
	NewContent   string       `json:"new_content,omitempty"`
	EditsApplied int          `json:"edits_applied"`
	EditsFailed  []FailedEdit `json:"edits_failed,omitempty"`
	// FileMutations is filled from the content the edit wrote. It is the
	// last field so the JSON is what withFileMutations produced by
	// appending the key to the marshalled metadata, without reading the
	// file back or re-parsing metadata that holds the whole file twice.
	FileMutations []fileMutation `json:"file_mutations,omitempty"`
}

const EditToolName = "edit"

//go:embed edit.md
var editDescription string

func NewEditTool(
	lspManager *lsp.Manager,
	files history.Service,
	filetracker filetracker.Service,
	workingDir string,
) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		EditToolName,
		editDescription,
		func(ctx context.Context, params EditParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			if len(params.Edits) == 0 {
				return fantasy.NewTextErrorResponse("at least one edit operation is required"), nil
			}

			params.FilePath = filepathext.SmartJoin(workingDir, params.FilePath)

			// Validate all edits before applying any
			if err := validateEdits(params.Edits); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			var response fantasy.ToolResponse
			var err error

			unlock := lockFile(params.FilePath)
			defer unlock()
			editCtx := editContext{ctx, files, filetracker, workingDir}
			// Handle file creation case (first edit has empty old_string)
			if params.Edits[0].OldString == "" {
				response, err = processEditWithCreation(editCtx, params)
			} else {
				response, err = processEditExistingFile(editCtx, params)
			}

			if err != nil {
				return response, err
			}

			if response.IsError {
				return response, nil
			}

			// Tell the language servers what changed and move on without
			// waiting. Their answer is relayed by the sweep before the next
			// model step, so a slow server never adds its analysis time to
			// an edit.
			lspManager.NotifyChangeAsync(ctx, params.FilePath)

			text := fmt.Sprintf("<result>\n%s\n</result>\n", response.Content)
			text += reportDiagnosticsNow(ctx, lspManager, params.FilePath)
			response.Content = text
			return response, nil
		},
	)
}

func validateEdits(edits []EditOperation) error {
	for i, edit := range edits {
		// Only the first edit can have empty old_string (for file creation)
		if i > 0 && edit.OldString == "" {
			return fmt.Errorf("edit %d: only the first edit can have empty old_string (for file creation)", i+1)
		}
		if len(edit.NewString) > maxEditNewStringBytes {
			return fmt.Errorf("edit %d: new_string is %d bytes, over the %d-byte cap; use the write tool for whole-file rewrites", i+1, len(edit.NewString), maxEditNewStringBytes)
		}
		if _, count := degenerateRepetition(edit.NewString, editRepetitionLimits); count > 0 {
			return fmt.Errorf("edit %d: new_string contains the same text repeated %d times, which looks like a corrupted payload; regenerate the edit and resend it", i+1, count)
		}
	}
	return nil
}

// applyEditsToContent applies edits sequentially, collecting the ones that
// failed. It also reports whether any edit only matched after whitespace
// normalization. An error means an applied edit failed the post-replacement
// verification: nothing downstream should be written. norm carries the
// normalized content from the evidence check through every edit and its
// verification; nil normalizes afresh each time.
func applyEditsToContent(norm *normCache, currentContent string, edits []EditOperation, startIndex int) (string, []FailedEdit, bool, error) {
	var failedEdits []FailedEdit
	var whitespaceCorrected bool
	for i, edit := range edits {
		newContent, corrected, err := applyEditToContent(norm, currentContent, edit)
		if err != nil {
			failedEdits = append(failedEdits, FailedEdit{
				Index: startIndex + i + 1,
				Error: err.Error(),
				Edit:  edit,
			})
			continue
		}
		if err := verifyReplacement(norm, newContent, edit, corrected); err != nil {
			return "", nil, false, fmt.Errorf("edit %d failed internal verification (%w); no changes were written, re-read the section and resend the edit", startIndex+i+1, err)
		}
		whitespaceCorrected = whitespaceCorrected || corrected
		currentContent = newContent
	}
	return currentContent, failedEdits, whitespaceCorrected, nil
}

// processEditWithCreation creates the file from the first edit, whose
// old_string is empty, and applies the rest to it.
func processEditWithCreation(edit editContext, params EditParams) (fantasy.ToolResponse, error) {
	firstEdit := params.Edits[0]

	// Check if file already exists
	if _, err := os.Stat(params.FilePath); err == nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("file already exists: %s", params.FilePath)), nil
	} else if !os.IsNotExist(err) {
		return fantasy.ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
	}

	// Create parent directories
	dir := filepath.Dir(params.FilePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("failed to create parent directories: %w", err)
	}

	currentContent, failedEdits, whitespaceCorrected, err := applyEditsToContent(&normCache{}, firstEdit.NewString, params.Edits[1:], 1)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	// Get session and message IDs
	sessionID, err := SessionIDOrError(edit.ctx, "creating a new file")
	if err != nil {
		return fantasy.ToolResponse{}, err
	}

	// Record the counts for the response metadata.
	additions, removals := diff.CountChanges("", currentContent)

	editsApplied := len(params.Edits) - len(failedEdits)

	// Write the file
	written := []byte(currentContent)
	err = guardedWrite(params.FilePath, nil, written, true)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	if err := recordFileVersion(edit.ctx, edit.files, sessionID, params.FilePath, "", currentContent); err != nil {
		return fantasy.ToolResponse{}, err
	}

	filetracker.Observe(edit.ctx, edit.filetracker, sessionID, params.FilePath, written, []filetracker.Range{{Start: 0, End: len(currentContent)}})

	var message string
	if len(failedEdits) > 0 {
		message = fmt.Sprintf("File created with %d of %d edits: %s (%d edit(s) failed)", editsApplied, len(params.Edits), params.FilePath, len(failedEdits))
	} else {
		message = fmt.Sprintf("File created with %d edits: %s", len(params.Edits), params.FilePath)
	}
	message = withWhitespaceNote(message, whitespaceCorrected)

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(message),
		EditResponseMetadata{
			OldContent:    "",
			NewContent:    currentContent,
			Additions:     additions,
			Removals:      removals,
			EditsApplied:  editsApplied,
			EditsFailed:   failedEdits,
			FileMutations: []fileMutation{newFileMutation(params.FilePath, written)},
		},
	), nil
}

func processEditExistingFile(edit editContext, params EditParams) (fantasy.ToolResponse, error) {
	sessionID, oldContent, isCrlf, toolErr, err := loadExistingFile(edit, params.FilePath, params.Edits[0].OldString)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if toolErr != nil {
		return *toolErr, nil
	}

	if err := validateEditSizes(oldContent, params.Edits); err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	norm := &normCache{}
	if err := checkEditRanges(edit, norm, sessionID, params.FilePath, oldContent, isCrlf, params.Edits); err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	currentContent, failedEdits, whitespaceCorrected, err := applyEditsToContent(norm, oldContent, params.Edits, 0)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	// Check if content actually changed
	if oldContent == currentContent {
		// If we have failed edits, report them
		if len(failedEdits) > 0 {
			return fantasy.WithResponseMetadata(
				fantasy.NewTextErrorResponse(fmt.Sprintf("no changes made - all %d edit(s) failed", len(failedEdits))),
				EditResponseMetadata{
					EditsApplied: 0,
					EditsFailed:  failedEdits,
				},
			), nil
		}
		return fantasy.NewTextErrorResponse("no changes made - all edits resulted in identical content"), nil
	}

	// Generate the counts for the response metadata.
	additions, removals := diff.CountChanges(oldContent, currentContent)

	editsApplied := len(params.Edits) - len(failedEdits)

	writeContent := currentContent
	if isCrlf {
		writeContent, _ = fsext.ToWindowsLineEndings(writeContent)
	}

	mutation, err := commitFileChange(edit, sessionID, params.FilePath, oldContent, writeContent, isCrlf)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	var message string
	if len(failedEdits) > 0 {
		message = fmt.Sprintf("Applied %d of %d edits to file: %s (%d edit(s) failed)", editsApplied, len(params.Edits), params.FilePath, len(failedEdits))
	} else {
		message = fmt.Sprintf("Applied %d edits to file: %s", len(params.Edits), params.FilePath)
	}
	message = withWhitespaceNote(message, whitespaceCorrected)

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(message),
		EditResponseMetadata{
			OldContent:    oldContent,
			NewContent:    currentContent,
			Additions:     additions,
			Removals:      removals,
			EditsApplied:  editsApplied,
			EditsFailed:   failedEdits,
			FileMutations: []fileMutation{mutation},
		},
	), nil
}

// applyEditToContent applies a single edit, reporting whether it only matched
// after whitespace normalization.
func applyEditToContent(norm *normCache, content string, edit EditOperation) (string, bool, error) {
	if edit.OldString == "" && edit.NewString == "" {
		return content, false, nil
	}

	if edit.OldString == "" {
		return "", false, fmt.Errorf("old_string cannot be empty for content replacement")
	}

	return findAndReplace(norm, content, edit.OldString, edit.NewString, edit.ReplaceAll)
}
