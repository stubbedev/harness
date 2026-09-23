package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"

	"charm.land/fantasy"

	"github.com/stubbedev/harness/internal/agent/prompt"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/subagents"
)

//go:embed templates/research.md
var researchToolDescription string

// researchValidationResult holds the validated parameters from the tool call context.
type researchValidationResult struct {
	SessionID      string
	AgentMessageID string
}

// validateResearchParams validates the tool call parameters and extracts required context values.
func validateResearchParams(ctx context.Context, params tools.ResearchParams) (researchValidationResult, error) {
	if params.Prompt == "" {
		return researchValidationResult{}, errors.New("prompt is required")
	}

	sessionID := tools.GetSessionFromContext(ctx)
	if sessionID == "" {
		return researchValidationResult{}, errors.New("session id missing from context")
	}

	agentMessageID := tools.GetMessageFromContext(ctx)
	if agentMessageID == "" {
		return researchValidationResult{}, errors.New("agent message id missing from context")
	}

	return researchValidationResult{
		SessionID:      sessionID,
		AgentMessageID: agentMessageID,
	}, nil
}

//go:embed templates/research_prompt.md.tpl
var researchPromptTmpl []byte

func (c *coordinator) researchTool(client *http.Client) fantasy.AgentTool {
	if client == nil {
		client = tools.DefaultHTTPClient()
	}

	return fantasy.NewParallelAgentTool(
		tools.ResearchToolName,
		researchToolDescription,
		func(ctx context.Context, params tools.ResearchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			validationResult, err := validateResearchParams(ctx, params)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			tmpDir, err := os.MkdirTemp(c.cfg.Config().Options.DataDirectory, "harness-fetch-*")
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to create temporary directory: %s", err)), nil
			}
			defer os.RemoveAll(tmpDir)

			var fullPrompt string

			if params.URL != "" {
				// URL mode: fetch the URL content first.
				res, err := tools.FetchURL(ctx, client, params.URL, tools.FetchFormatMarkdown)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to fetch URL: %s", err)), nil
				}
				if res.Binary {
					return fantasy.NewTextErrorResponse(fmt.Sprintf(
						"The response from %s is not text (%s, %d bytes); use the download tool to save it instead.",
						params.URL, res.ContentType, res.Size)), nil
				}
				content := res.Content

				hasLargeContent := len(content) > tools.LargeContentThreshold

				if hasLargeContent {
					tempFile, err := os.CreateTemp(tmpDir, "page-*.md")
					if err != nil {
						return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to create temporary file: %s", err)), nil
					}
					tempFilePath := tempFile.Name()

					if _, err := tempFile.WriteString(content); err != nil {
						tempFile.Close()
						return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to write content to file: %s", err)), nil
					}
					tempFile.Close()

					fullPrompt = fmt.Sprintf("%s\n\nThe web page from %s has been saved to: %s\n\nUse the view and shell tools to analyze this file and extract the requested information.", params.Prompt, params.URL, tempFilePath)
				} else {
					fullPrompt = fmt.Sprintf("%s\n\nWeb page URL: %s\n\n<webpage_content>\n%s\n</webpage_content>", params.Prompt, params.URL, content)
				}
			} else {
				// Search mode: let the sub-agent search and fetch as needed.
				fullPrompt = fmt.Sprintf("%s\n\nUse the web_search tool to find relevant information. Break down the question into smaller, focused searches if needed. After searching, use fetch to get detailed content from the most relevant results.", params.Prompt)
			}

			promptOpts := []prompt.Option{
				prompt.WithWorkingDir(tmpDir),
			}

			promptTemplate, err := prompt.NewPrompt("research", string(researchPromptTmpl), promptOpts...)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error creating prompt: %s", err)
			}

			_, small, err := c.buildAgentModels(ctx, true)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error building models: %s", err)
			}

			systemPrompt, err := promptTemplate.Build(ctx, small.Model.Provider(), small.Model.Model(), c.cfg)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error building system prompt: %s", err)
			}

			smallProviderCfg, ok := c.cfg.Config().Providers.Get(small.ModelCfg.Provider)
			if !ok {
				return fantasy.ToolResponse{}, errors.New("small model provider not configured")
			}

			fetchTools := []fantasy.AgentTool{
				tools.NewFetchTool(client),
				tools.NewWebSearchTool(client),
				tools.NewViewTool(c.lspManager, c.filetracker, nil, tmpDir),
			}
			// Searching the saved pages is the shell's job, but only
			// when there is a shell to give it.
			if tools.ShellAvailable() {
				fetchTools = append(fetchTools,
					tools.NewShellTool(tmpDir, "research", c.questions))
			}

			// Sub-agent tool calls fire the same Pre/PostToolUse hooks as the
			// top-level agent's; the hooks see this run's (child) session ID.
			fetchTools = wrapToolsWithHooks(wrapToolsResilient(fetchTools), c.hooks, c.queueArrivalEpoch)

			agent := NewSessionAgent(SessionAgentOptions{
				Config:               c.cfg,
				LargeModel:           small, // Use small model for both (fetch doesn't need large)
				SmallModel:           small,
				SystemPromptPrefix:   smallProviderCfg.SystemPromptPrefix,
				SystemPrompt:         systemPrompt,
				DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
				AutoSummarizeRatio:   c.cfg.Config().Options.AutoSummarizeRatio,
				AutoSummarizeBuffer:  c.cfg.Config().Options.AutoSummarizeBuffer,
				MaxRetries:           c.cfg.Config().Options.MaxRetries,
				Sessions:             c.sessions,
				Messages:             c.messages,
				Tools:                fetchTools,
			})

			return c.runSubAgent(ctx, subAgentParams{
				Agent:          agent,
				SessionID:      validationResult.SessionID,
				AgentMessageID: validationResult.AgentMessageID,
				ToolCallID:     call.ID,
				Prompt:         fullPrompt,
				SessionTitle:   "Fetch Analysis",
				AgentName:      tools.ResearchToolName,
				AgentColor:     subagents.AutoColor(tools.ResearchToolName),
				AgentModel:     agent.Model().ModelCfg.Model,
			})
		},
	)
}
