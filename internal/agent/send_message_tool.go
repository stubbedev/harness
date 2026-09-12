package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/csync"
)

// SendMessageToolName is the tool sub-agents use to message their
// orchestrator mid-run.
const SendMessageToolName = "send_message"

// sendMessageTool lets a dispatched sub-agent push a message to the
// orchestrating agent while it is still running. Messages cannot interrupt
// the orchestrator mid-turn (it is blocked waiting for the dispatch to
// return), so they are collected in a per-run inbox and appended to the
// dispatch tool's result — guaranteed delivery even when the sub-agent
// later fails, times out, or is cancelled. That makes it the right channel
// for partial findings and for questions whose answers are not needed to
// continue; the sub-agent's final text output remains its main report.
type sendMessageTool struct {
	coord *coordinator
	opts  fantasy.ProviderOptions
}

func (t *sendMessageTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name: SendMessageToolName,
		Description: `Send a message to the orchestrating agent that dispatched you.

The orchestrator reads messages when your run completes, so do not wait for a reply. Use this for:
- Partial findings worth keeping even if the rest of your run fails.
- Questions or caveats the orchestrator should weigh when combining your results with other agents'.

Your final response text is still your primary report; do not duplicate it here.`,
		Parameters: map[string]any{
			"message": map[string]any{
				"type":        "string",
				"description": "The message for the orchestrator. Concise and self-contained.",
			},
		},
		Required: []string{"message"},
	}
}

func (t *sendMessageTool) ProviderOptions() fantasy.ProviderOptions        { return t.opts }
func (t *sendMessageTool) SetProviderOptions(opts fantasy.ProviderOptions) { t.opts = opts }

func (t *sendMessageTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	sessionID := tools.GetSessionFromContext(ctx)
	if sessionID == "" {
		return fantasy.NewTextErrorResponse("session id missing from context"), nil
	}
	var params struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.NewTextErrorResponse("invalid parameters: " + err.Error()), nil
	}
	message := strings.TrimSpace(params.Message)
	if message == "" {
		return fantasy.NewTextErrorResponse(`"message" must not be empty`), nil
	}
	t.coord.recordSubagentMessage(sessionID, message)
	return fantasy.NewTextResponse("Message delivered to the orchestrator; it will be read when your run completes."), nil
}

// recordSubagentMessage appends to the per-run inbox of a child session.
// The inbox is optional: a coordinator built without one (tests that
// exercise a single dispatch path) just drops the message.
func (c *coordinator) recordSubagentMessage(childSessionID, message string) {
	if c.subagentMessages == nil {
		return
	}
	existing, _ := c.subagentMessages.Get(childSessionID)
	c.subagentMessages.Set(childSessionID, append(existing, message))
}

// drainSubagentMessages returns and clears the inbox of a child session.
func (c *coordinator) drainSubagentMessages(childSessionID string) []string {
	if c.subagentMessages == nil {
		return nil
	}
	msgs, ok := c.subagentMessages.Get(childSessionID)
	if ok {
		c.subagentMessages.Del(childSessionID)
	}
	return msgs
}

// appendSubagentMessages renders drained inbox messages into the dispatch
// tool response the orchestrator reads.
func appendSubagentMessages(resp *fantasy.ToolResponse, messages []string) {
	if resp == nil || len(messages) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("\n\n## Messages sent during this run\n")
	for i, msg := range messages {
		fmt.Fprintf(&b, "%d. %s\n", i+1, msg)
	}
	resp.Content += b.String()
}

// newSubagentInbox builds the per-run inbox the coordinator hands to
// recordSubagentMessage and drainSubagentMessages.
func newSubagentInbox() *csync.Map[string, []string] {
	return csync.NewMap[string, []string]()
}
