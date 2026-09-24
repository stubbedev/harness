package tools

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"charm.land/fantasy"
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

// findAndReplace performs a find-and-replace on content. When replaceAll is
// false it requires exactly one match. If an exact match fails, it falls back
// to whitespace-normalized matching and, failing that, returns a diagnostic
// hint describing why the replacement could not be made. The returned boolean
// reports whether the replacement relied on the whitespace-normalized
// fallback rather than an exact match. The fallback and the hint share the
// normalized content held in norm, and a fallback replacement leaves the
// normalized form of its result there.
func findAndReplace(norm *normCache, content, old, new string, replaceAll bool) (string, bool, error) {
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
			return "", false, fmt.Errorf("old_string appears multiple times in the file%s. Please provide more context to ensure a unique match, or set replace_all to true", ambiguityHint(content, old))
		default:
			return content[:index] + new + content[index+len(old):], false, nil
		}
	}

	nc := norm.of(content)
	matches := nc.matches(old)
	if result, ok := replaceNormalized(nc, matches, old, new, replaceAll); ok {
		norm.remember(result)
		return result.content, true, nil
	}
	return "", false, notFoundError(nc, matches, old)
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
func notFoundError(nc *normalizedContent, matches []normMatch, old string) error {
	msg := "old_string not found in file. Make sure it matches exactly, including whitespace and line breaks"
	if hint := mismatchHint(nc, matches, old); hint != "" {
		msg += "\n\n" + hint
	}
	return errors.New(msg)
}

// ambiguityHint lists the line numbers of the first occurrences of old, so
// the caller can add surrounding context instead of guessing where the
// matches are. It returns "" when old occurs at most once.
func ambiguityHint(content, old string) string {
	line, offset := 1, 0
	var found []int
	for len(found) < 3 {
		idx := strings.Index(content[offset:], old)
		if idx < 0 {
			break
		}
		pos := offset + idx
		line += strings.Count(content[offset:pos], "\n")
		found = append(found, line)
		line += strings.Count(old, "\n")
		offset = pos + len(old)
	}
	if len(found) < 2 {
		return ""
	}
	list := make([]string, len(found))
	for i, n := range found {
		list[i] = fmt.Sprintf("line %d", n)
	}
	hint := fmt.Sprintf(" Found at %s", strings.Join(list, ", "))
	if strings.Contains(content[offset:], old) {
		hint += ", and more"
	}
	return hint
}

// commitFileChange writes newContent to filePath, updates the file history,
// and records the read in the file tracker. Callers must convert line endings
// before calling this function.
func commitFileChange(edit editContext, sessionID, filePath, oldContent, newContent string, crlf bool) error {
	current, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	expected := oldContent
	if crlf {
		expected, _ = fsext.ToWindowsLineEndings(oldContent)
	}
	if string(current) != expected {
		return conflictEvidence(edit.ctx, edit.filetracker, sessionID, filePath, current, 0, filetracker.ErrStale)
	}
	oldContent = string(current)
	ranges := changedRanges(oldContent, newContent)
	if err := checkFileEvidence(edit.ctx, edit.filetracker, sessionID, filePath, current, ranges); err != nil {
		at := 0
		if len(ranges) > 0 {
			at = ranges[0].Start
		}
		return conflictEvidence(edit.ctx, edit.filetracker, sessionID, filePath, current, at, err)
	}

	if err := guardedWrite(filePath, []byte(oldContent), []byte(newContent), false); err != nil {
		if current, readErr := os.ReadFile(filePath); readErr == nil {
			return conflictEvidence(edit.ctx, edit.filetracker, sessionID, filePath, current, 0, err)
		}
		return fmt.Errorf("failed to write file: %w", err)
	}

	if err := recordFileVersion(edit.ctx, edit.files, sessionID, filePath, oldContent, newContent); err != nil {
		return err
	}

	filetracker.Advance(edit.ctx, edit.filetracker, sessionID, filePath, []byte(oldContent), []byte(newContent))
	return nil
}

// recordFileVersion stores filePath in the session's file history: the
// initial entry when the path is untracked, an intermediate version
// when the tracked content differs from oldContent (the user edited the
// file out of band), and a version holding newContent. Every tool that
// changes a file records it through here, so rewind sees the same
// history whichever tool made the change. Version failures are logged,
// not fatal; failing to read or create the entry is.
func recordFileVersion(ctx context.Context, files history.Service, sessionID, filePath, oldContent, newContent string) error {
	file, err := files.GetByPathAndSession(ctx, filePath, sessionID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := files.Create(ctx, sessionID, filePath, oldContent); err != nil {
			return fmt.Errorf("error creating file history: %w", err)
		}
	case err != nil:
		return fmt.Errorf("error reading file history: %w", err)
	case file.Content != oldContent:
		// User manually changed the content; store an intermediate version.
		if _, err := files.CreateVersion(ctx, sessionID, filePath, oldContent); err != nil {
			slog.Error("Error creating file history version", "error", err)
		}
	}
	if _, err := files.CreateVersion(ctx, sessionID, filePath, newContent); err != nil {
		slog.Error("Error creating file history version", "error", err)
	}
	return nil
}

// loadExistingFile reads a file the edit tool is about to change, in Unix
// line endings, and checks the session's evidence for it. A problem the
// model should hear about comes back as toolErr; err is for failures the
// tool cannot report as a result. hint is text the edit expects to find,
// which positions a conflict report near it.
func loadExistingFile(edit editContext, filePath, hint string) (sessionID, oldContent string, isCrlf bool, toolErr *fantasy.ToolResponse, err error) {
	reject := func(msg string) (string, string, bool, *fantasy.ToolResponse, error) {
		resp := fantasy.NewTextErrorResponse(msg)
		return "", "", false, &resp, nil
	}
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return reject(fmt.Sprintf("file not found: %s", filePath))
		}
		return "", "", false, nil, fmt.Errorf("failed to access file: %w", err)
	}

	if fileInfo.IsDir() {
		return reject(fmt.Sprintf("path is a directory, not a file: %s", filePath))
	}

	sessionID, err = SessionIDOrError(edit.ctx, "editing a file")
	if err != nil {
		return "", "", false, nil, err
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", false, nil, fmt.Errorf("failed to read file: %w", err)
	}

	if checkErr := checkFileEvidence(edit.ctx, edit.filetracker, sessionID, filePath, content, nil); checkErr != nil {
		at := max(0, strings.Index(string(content), hint))
		return reject(conflictEvidence(edit.ctx, edit.filetracker, sessionID, filePath, content, at, checkErr).Error())
	}

	oldContent, isCrlf = fsext.ToUnixLineEndings(string(content))
	return sessionID, oldContent, isCrlf, nil, nil
}
