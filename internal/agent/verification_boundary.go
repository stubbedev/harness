package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/google/uuid"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/verification"
)

func verificationChangedPaths(ctx context.Context, root, sessionID string, files history.Service) ([]string, error) {
	var paths []string
	if files != nil {
		changed, err := files.ListBySessionWithChildren(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		for _, file := range changed {
			paths = append(paths, file.Path)
		}
	}
	probe := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	probe.Dir = root
	if err := probe.Run(); err == nil {
		for _, args := range [][]string{{"diff", "--name-only", "-z"}, {"diff", "--cached", "--name-only", "-z"}, {"ls-files", "--others", "--exclude-standard", "-z"}} {
			command := exec.CommandContext(ctx, "git", args...)
			command.Dir = root
			output, err := command.Output()
			if err != nil {
				return nil, fmt.Errorf("list changed workspace files: %w", err)
			}
			for path := range strings.SplitSeq(string(output), "\x00") {
				if path != "" {
					paths = append(paths, path)
				}
			}
		}
	} else if files == nil {
		return nil, fmt.Errorf("cannot determine changed paths without file history or a Git workspace")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

func (c *coordinator) verificationTool() (fantasy.AgentTool, error) {
	runner, err := verification.New(c.cfg.WorkingDir(), c.cfg.Config().Verification)
	if err != nil {
		return nil, err
	}
	return NewVerificationTool(runner, func(ctx context.Context) ([]string, error) {
		return verificationChangedPaths(ctx, c.cfg.WorkingDir(), tools.GetSessionFromContext(ctx), c.history)
	}, nil), nil
}

func latestVerification(msgs []message.Message) *verification.Result {
	var latest *verification.Result
	for _, msg := range msgs {
		for _, result := range msg.ToolResults() {
			if result.Name != VerificationToolName {
				continue
			}
			var metadata struct {
				Verification *verification.Result `json:"verification"`
			}
			if json.Unmarshal([]byte(result.Metadata), &metadata) == nil && metadata.Verification != nil {
				latest = metadata.Verification
			}
		}
	}
	return latest
}

func (a *sessionAgent) completionVerification(ctx context.Context, sessionID string, repairAttempts int) (verification.Decision, error) {
	if a.isSubAgent || a.cfg == nil || !a.cfg.Config().Verification.RequireOnCompletion {
		return verification.Decision{Allow: true}, nil
	}
	runner, err := verification.New(a.cfg.WorkingDir(), a.cfg.Config().Verification)
	if err != nil {
		return verification.Decision{}, err
	}
	paths, err := verificationChangedPaths(ctx, a.cfg.WorkingDir(), sessionID, a.files)
	if err != nil {
		return verification.Decision{}, err
	}
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return verification.Decision{}, err
	}
	return runner.Gate(ctx, paths, latestVerification(msgs), repairAttempts), nil
}

func (a *sessionAgent) runCompletionVerification(ctx context.Context, sessionID string) error {
	var verifier fantasy.AgentTool
	for _, tool := range a.tools.Copy() {
		if tool.Info().Name == VerificationToolName {
			verifier = tool
			break
		}
	}
	if verifier == nil {
		return fmt.Errorf("verification required but verify tool is unavailable")
	}
	callID := uuid.NewString()
	callMessage, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.ToolCall{ID: callID, Name: VerificationToolName, Input: "{}", Finished: true}},
	})
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, callMessage.ID)
	response, err := verifier.Run(ctx, fantasy.ToolCall{ID: callID, Name: VerificationToolName, Input: "{}"})
	if err != nil {
		return err
	}
	_, err = a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: callID, Name: VerificationToolName, Content: response.Content, Metadata: response.Metadata, IsError: response.IsError}},
	})
	if err != nil {
		return err
	}
	var evidence struct {
		Verification *verification.Result `json:"verification"`
	}
	if response.StopTurn || json.Unmarshal([]byte(response.Metadata), &evidence) != nil || evidence.Verification == nil {
		return fmt.Errorf("verification blocked: %s", response.Content)
	}
	return nil
}
