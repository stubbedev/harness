package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/diff"
	"github.com/stubbedev/harness/internal/filepathext"
	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/history"

	"github.com/stubbedev/harness/internal/lsp"
)

//go:embed write.md
var writeDescription string

type WriteParams struct {
	FilePath string `json:"file_path" description:"The path to the file to write"`
	Content  string `json:"content" description:"The content to write to the file"`
}

type WriteResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
	// FileMutations is filled from the content the write put on disk, as
	// the last field so the JSON matches what withFileMutations appended.
	FileMutations []fileMutation `json:"file_mutations,omitempty"`
}

const WriteToolName = "write"

func NewWriteTool(
	lspManager *lsp.Manager,
	files history.Service,
	tracker filetracker.Service,
	workingDir string,
) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		WriteToolName,
		writeDescription,
		func(ctx context.Context, params WriteParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			if err := validateWritePayload(params.Content); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			sessionID, err := SessionIDOrError(ctx, "writing files")
			if err != nil {
				return fantasy.ToolResponse{}, err
			}

			filePath := filepathext.SmartJoin(workingDir, params.FilePath)

			unlock := lockFile(filePath)
			defer unlock()
			oldBytes, err := os.ReadFile(filePath)
			create := os.IsNotExist(err)
			if err != nil && !create {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error reading file: %s", err)), nil
			}
			oldContent := string(oldBytes)
			if !create {
				if err := checkFileEvidence(ctx, tracker, sessionID, filePath, oldBytes, []filetracker.Range{{Start: 0, End: len(oldBytes)}}); err != nil {
					return fantasy.NewTextErrorResponse(conflictEvidence(ctx, tracker, sessionID, filePath, oldBytes, 0, err).Error()), nil
				}
				if oldContent == params.Content {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("File %s already contains the exact content. No changes made.", filePath)), nil
				}
			}
			if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
				return fantasy.ToolResponse{}, err
			}

			diff, additions, removals := diff.GenerateDiff(
				oldContent,
				params.Content,
				strings.TrimPrefix(filePath, workingDir),
			)

			written := []byte(params.Content)
			err = guardedWrite(filePath, oldBytes, written, create)
			if err != nil {
				if current, readErr := os.ReadFile(filePath); readErr == nil {
					err = conflictEvidence(ctx, tracker, sessionID, filePath, current, 0, err)
				}
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			if err := recordFileVersion(ctx, files, sessionID, filePath, oldContent, params.Content); err != nil {
				return fantasy.ToolResponse{}, err
			}

			filetracker.Observe(ctx, tracker, sessionID, filePath, written, []filetracker.Range{{Start: 0, End: len(params.Content)}})

			lspManager.NotifyChangeAsync(ctx, filePath)

			result := fmt.Sprintf("File successfully written: %s", filePath)
			result = fmt.Sprintf("<result>\n%s\n</result>", result)
			result += reportDiagnosticsNow(ctx, lspManager, filePath)
			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(result),
				WriteResponseMetadata{
					Diff:          diff,
					Additions:     additions,
					Removals:      removals,
					FileMutations: []fileMutation{newFileMutation(filePath, written)},
				},
			), nil
		},
	)
}
