package tools

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/diff"
	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/history"
)

type editContext struct {
	ctx         context.Context
	files       history.Service
	filetracker filetracker.Service
	workingDir  string
}

func createNewFile(edit editContext, filePath, content string, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	fileInfo, err := os.Stat(filePath)
	if err == nil {
		if fileInfo.IsDir() {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("file already exists: %s", filePath)), nil
	} else if !os.IsNotExist(err) {
		return fantasy.ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
	}

	dir := filepath.Dir(filePath)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("failed to create parent directories: %w", err)
	}

	sessionID := GetSessionFromContext(edit.ctx)
	if sessionID == "" {
		return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for creating a new file")
	}

	_, additions, removals := diff.GenerateDiff(
		"",
		content,
		strings.TrimPrefix(filePath, edit.workingDir),
	)

	err = os.WriteFile(filePath, []byte(content), 0o644)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	// File can't be in the history so we create a new file history
	_, err = edit.files.Create(edit.ctx, sessionID, filePath, "")
	if err != nil {
		// Log error but don't fail the operation
		return fantasy.ToolResponse{}, fmt.Errorf("error creating file history: %w", err)
	}

	// Add the new content to the file history
	_, err = edit.files.CreateVersion(edit.ctx, sessionID, filePath, content)
	if err != nil {
		// Log error but don't fail the operation
		slog.Error("Error creating file history version", "error", err)
	}

	edit.filetracker.RecordRead(edit.ctx, sessionID, filePath)

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse("File created: "+filePath),
		EditResponseMetadata{
			OldContent: "",
			NewContent: content,
			Additions:  additions,
			Removals:   removals,
		},
	), nil
}

// findAndReplace performs a find-and-replace on content. When replaceAll is
// false it requires exactly one match. If an exact match fails, it falls back
// to whitespace-normalized matching and, failing that, returns a diagnostic
// hint describing why the replacement could not be made. The returned boolean
// reports whether the replacement relied on the whitespace-normalized
// fallback rather than an exact match.
func findAndReplace(content, old, new string, replaceAll bool) (string, bool, error) {
	if replaceAll {
		if strings.Contains(content, old) {
			return strings.ReplaceAll(content, old, new), false, nil
		}
	} else {
		index := strings.Index(content, old)
		switch {
		case index == -1:
			// Fall through to the fuzzy fallback below.
		case index != strings.LastIndex(content, old):
			return "", false, fmt.Errorf("old_string appears multiple times in the file. Please provide more context to ensure a unique match, or set replace_all to true")
		default:
			return content[:index] + new + content[index+len(old):], false, nil
		}
	}

	if result, ok := normalizedReplace(content, old, new, replaceAll); ok {
		return result, true, nil
	}
	return "", false, notFoundError(content, old)
}

// withWhitespaceNote appends the whitespace auto-correction note to a tool
// response message when the edit did not match the file byte-for-byte.
func withWhitespaceNote(message string, whitespaceCorrected bool) string {
	if !whitespaceCorrected {
		return message
	}
	return message + "\n" + whitespaceCorrectedNote
}

// notFoundError builds the "old_string not found" error, appending a
// diagnostic hint when one is available to help the caller self-correct.
func notFoundError(content, old string) error {
	msg := "old_string not found in file. Make sure it matches exactly, including whitespace and line breaks"
	if hint := diagnoseMismatch(content, old); hint != "" {
		msg += "\n\n" + hint
	}
	return errors.New(msg)
}

// commitFileChange writes newContent to filePath, updates the file history,
// and records the read in the file tracker. Callers must convert line endings
// before calling this function.
func commitFileChange(edit editContext, sessionID, filePath, oldContent, newContent string) error {
	if err := os.WriteFile(filePath, []byte(newContent), 0o644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	file, err := edit.files.GetByPathAndSession(edit.ctx, filePath, sessionID)
	if err != nil {
		_, err = edit.files.Create(edit.ctx, sessionID, filePath, oldContent)
		if err != nil {
			return fmt.Errorf("error creating file history: %w", err)
		}
	}
	if file.Content != oldContent {
		// User manually changed the content; store an intermediate version.
		if _, err := edit.files.CreateVersion(edit.ctx, sessionID, filePath, oldContent); err != nil {
			slog.Error("Error creating file history version", "error", err)
		}
	}
	if _, err := edit.files.CreateVersion(edit.ctx, sessionID, filePath, newContent); err != nil {
		slog.Error("Error creating file history version", "error", err)
	}

	edit.filetracker.RecordRead(edit.ctx, sessionID, filePath)
	return nil
}

func loadExistingFile(edit editContext, filePath, sessionError string) (sessionID, oldContent string, isCrlf bool, resp fantasy.ToolResponse, err error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", false, fantasy.NewTextErrorResponse(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return "", "", false, fantasy.ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
	}

	if fileInfo.IsDir() {
		return "", "", false, fantasy.NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
	}

	sessionID = GetSessionFromContext(edit.ctx)
	if sessionID == "" {
		return "", "", false, fantasy.ToolResponse{}, fmt.Errorf("%s", sessionError)
	}

	lastRead := edit.filetracker.LastReadTime(edit.ctx, sessionID, filePath)
	if lastRead.IsZero() {
		return "", "", false, fantasy.NewTextErrorResponse("you must read the file before editing it. Use the View tool first"), nil
	}

	modTime := fileInfo.ModTime().Truncate(time.Second)
	if modTime.After(lastRead) {
		return "", "", false, fantasy.NewTextErrorResponse(
			fmt.Sprintf(
				"file %s has been modified since it was last read (mod time: %s, last read: %s)",
				filePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
			),
		), nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", false, fantasy.ToolResponse{}, fmt.Errorf("failed to read file: %w", err)
	}

	oldContent, isCrlf = fsext.ToUnixLineEndings(string(content))
	return sessionID, oldContent, isCrlf, fantasy.ToolResponse{}, nil
}

func deleteContent(edit editContext, filePath, oldString string, replaceAll bool, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	sessionID, oldContent, isCrlf, resp, err := loadExistingFile(edit, filePath, "session ID is required for deleting content")
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if resp.Content != "" || resp.IsError {
		return resp, nil
	}

	newContent, whitespaceCorrected, err := findAndReplace(oldContent, oldString, "", replaceAll)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	_, additions, removals := diff.GenerateDiff(
		oldContent,
		newContent,
		strings.TrimPrefix(filePath, edit.workingDir),
	)

	writeContent := newContent
	if isCrlf {
		writeContent, _ = fsext.ToWindowsLineEndings(writeContent)
	}

	if err := commitFileChange(edit, sessionID, filePath, oldContent, writeContent); err != nil {
		return fantasy.ToolResponse{}, err
	}

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(withWhitespaceNote("Content deleted from file: "+filePath, whitespaceCorrected)),
		EditResponseMetadata{
			OldContent: oldContent,
			NewContent: writeContent,
			Additions:  additions,
			Removals:   removals,
		},
	), nil
}

func replaceContent(edit editContext, filePath, oldString, newString string, replaceAll bool, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	sessionID, oldContent, isCrlf, resp, err := loadExistingFile(edit, filePath, "session ID is required for editing a file")
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if resp.Content != "" || resp.IsError {
		return resp, nil
	}

	result, whitespaceCorrected, err := findAndReplace(oldContent, oldString, newString, replaceAll)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	if result == oldContent {
		return fantasy.NewTextErrorResponse("new content is the same as old content. No changes made."), nil
	}

	_, additions, removals := diff.GenerateDiff(
		oldContent,
		result,
		strings.TrimPrefix(filePath, edit.workingDir),
	)

	writeContent := result
	if isCrlf {
		writeContent, _ = fsext.ToWindowsLineEndings(writeContent)
	}

	if err := commitFileChange(edit, sessionID, filePath, oldContent, writeContent); err != nil {
		return fantasy.ToolResponse{}, err
	}

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(withWhitespaceNote("Content replaced in file: "+filePath, whitespaceCorrected)),
		EditResponseMetadata{
			OldContent: oldContent,
			NewContent: writeContent,
			Additions:  additions,
			Removals:   removals,
		},
	), nil
}
