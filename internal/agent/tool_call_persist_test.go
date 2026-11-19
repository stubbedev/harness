package agent

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/session"
)

var toolCallIDPattern = regexp.MustCompile(`"tool_call_id":"([^"]+)"`)

// orderingQuerier records what reaches SQL: how often each message is
// rewritten, and whether a tool result row ever lands before a stored
// assistant message holds the call it answers.
type orderingQuerier struct {
	db.Querier
	mu       sync.Mutex
	writes   map[string]int
	stored   map[string]string
	orphaned []string
}

func (q *orderingQuerier) UpdateMessage(ctx context.Context, arg db.UpdateMessageParams) (int64, error) {
	rows, err := q.Querier.UpdateMessage(ctx, arg)
	if err != nil {
		return rows, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.writes[arg.ID]++
	q.stored[arg.ID] = arg.Parts
	return rows, nil
}

func (q *orderingQuerier) CreateMessage(ctx context.Context, arg db.CreateMessageParams) (db.Message, error) {
	if arg.Role == string(message.Tool) {
		q.mu.Lock()
		for _, match := range toolCallIDPattern.FindAllStringSubmatch(arg.Parts, -1) {
			held := false
			for _, parts := range q.stored {
				if strings.Contains(parts, `"`+match[1]+`"`) {
					held = true
					break
				}
			}
			if !held {
				q.orphaned = append(q.orphaned, match[1])
			}
		}
		q.mu.Unlock()
	}
	return q.Querier.CreateMessage(ctx, arg)
}

// A batch of tool calls reaches the transcript one call at a time, but
// the assistant message is written a bounded number of times rather
// than once per call, and always ahead of the results that answer it.
func TestToolCallBatchIsWrittenOnceAheadOfItsResults(t *testing.T) {
	const calls = 12
	conn, err := db.Connect(t.Context(), tempDBDir(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	recorder := &orderingQuerier{Querier: q, writes: map[string]int{}, stored: map[string]string{}}
	messages := message.NewService(recorder)

	scripted := make([]scriptedCall, calls)
	for i := range scripted {
		scripted[i] = scriptedCall{name: "probe", input: map[string]any{"n": i}}
	}
	model := newScriptedModel(scriptedTurn{calls: scripted})
	agent := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{
			Model:      model,
			CatalogCfg: catalog.Model{ID: "large", ContextWindow: 1_000_000, DefaultMaxTokens: 1000},
			ModelCfg:   config.SelectedModel{Model: "large", Provider: "scripted"},
		},
		SmallModel:   Model{Model: textModel("title")},
		SystemPrompt: "system",
		Sessions:     sessions,
		Messages:     messages,
		Tools:        []fantasy.AgentTool{newProbeTool(t, calls, true)},
	})
	sess, err := sessions.Create(t.Context(), "batch")
	require.NoError(t, err)

	// Watch what subscribers see of the assistant message.
	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sub := messages.Subscribe(subCtx)
	var seenMu sync.Mutex
	seen := map[int]bool{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-subCtx.Done():
				return
			case ev := <-sub:
				if ev.Type != pubsub.UpdatedEvent || ev.Payload.Role != message.Assistant {
					continue
				}
				finished := 0
				for _, call := range ev.Payload.ToolCalls() {
					if call.Finished {
						finished++
					}
				}
				seenMu.Lock()
				seen[finished] = true
				seenMu.Unlock()
			}
		}
	}()

	_, err = agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "probe"})
	require.NoError(t, err)

	msgs, err := messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var assistant message.Message
	results := 0
	for _, m := range msgs {
		switch m.Role {
		case message.Assistant:
			if len(m.ToolCalls()) > 0 {
				assistant = m
			}
		case message.Tool:
			results += len(m.ToolResults())
		}
	}
	require.Len(t, assistant.ToolCalls(), calls)
	require.Equal(t, calls, results)

	recorder.mu.Lock()
	writes := recorder.writes[assistant.ID]
	orphaned := recorder.orphaned
	recorder.mu.Unlock()
	require.Empty(t, orphaned, "tool results stored before the message holding their calls")
	// The burst, the debounce tick at most, and the step's finish.
	require.LessOrEqual(t, writes, 3, "assistant message rewritten per tool call")

	cancel()
	<-done
	seenMu.Lock()
	defer seenMu.Unlock()
	for i := 1; i <= calls; i++ {
		require.True(t, seen[i], "subscribers never saw the message with %d finished calls", i)
	}
}

// The assistant message holding a round's calls is stored before any of
// them runs, so a process that dies while a tool changes something leaves
// the call that changed it behind. The debounce is long enough that only
// the dispatch can have written it.
func TestToolCallIsStoredBeforeTheToolRuns(t *testing.T) {
	t.Parallel()
	conn, err := db.Connect(t.Context(), tempDBDir(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q, message.WithDebounce(time.Hour))

	type args struct {
		Path string `json:"path"`
	}
	var storedBeforeRun []message.ToolCall
	mutate := fantasy.NewAgentTool("mutate", "changes a file",
		func(ctx context.Context, in args, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			// A service of its own holds nothing buffered: what it reads
			// is what the database holds.
			id, _ := ctx.Value(tools.MessageIDContextKey).(string)
			stored, err := message.NewService(q).Get(ctx, id)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			storedBeforeRun = stored.ToolCalls()
			return fantasy.NewTextResponse("changed " + in.Path), nil
		})

	model := newScriptedModel(scriptedTurn{calls: []scriptedCall{{name: "mutate", input: map[string]any{"path": "a.go"}}}})
	agent := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{
			Model:      model,
			CatalogCfg: catalog.Model{ID: "large", ContextWindow: 1_000_000, DefaultMaxTokens: 1000},
			ModelCfg:   config.SelectedModel{Model: "large", Provider: "scripted"},
		},
		SmallModel:   Model{Model: textModel("title")},
		SystemPrompt: "system",
		Sessions:     sessions,
		Messages:     messages,
		Tools:        []fantasy.AgentTool{mutate},
	})
	sess, err := sessions.Create(t.Context(), "dispatch")
	require.NoError(t, err)

	_, err = agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "change a.go"})
	require.NoError(t, err)

	require.Len(t, storedBeforeRun, 1)
	require.Equal(t, "mutate", storedBeforeRun[0].Name)
	require.True(t, storedBeforeRun[0].Finished)
	require.JSONEq(t, `{"path":"a.go"}`, storedBeforeRun[0].Input)
}
