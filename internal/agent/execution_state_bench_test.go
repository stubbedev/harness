package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stubbedev/harness/internal/message"
)

// benchmarkToolOutput is a tool result body of roughly size bytes that
// looks like source code, which is what most large results are.
func benchmarkToolOutput(size, seed int) string {
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "func handler%d_%d(ctx context.Context, req *Request) (*Response, error) { return nil, nil }\n", seed, i)
	}
	return b.String()[:size]
}

// benchmarkExecutionHistory is about a megabyte of history: forty shell
// results of 25 KB each and one 256 KB screenshot.
func benchmarkExecutionHistory() []message.Message {
	var msgs []message.Message
	for i := range 40 {
		id := fmt.Sprintf("call-%d", i)
		msgs = append(msgs,
			message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: id, Name: "shell", Input: fmt.Sprintf(`{"command":"go test ./pkg%d/..."}`, i), Finished: true}}},
			message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: id, Name: "shell", Content: benchmarkToolOutput(25<<10, i), Metadata: `{"exit_code":0,"session":"main"}`}}},
		)
	}
	msgs = append(msgs,
		message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "shot", Name: "view", Input: `{"file_path":"/tmp/shot.png"}`, Finished: true}}},
		message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "shot", Name: "view", Data: strings.Repeat("iVBORw0KGgo", (256<<10)/11), MIMEType: "image/png"}}},
	)
	return msgs
}

// BenchmarkExecutionStateIngestResult is the per-tool-result cost paid
// in OnToolResult: one fresh 100 KB result.
func BenchmarkExecutionStateIngestResult(b *testing.B) {
	content := benchmarkToolOutput(100<<10, 0)
	state := newExecutionState("")
	b.ReportAllocs()
	b.SetBytes(int64(len(content)))
	for i := 0; b.Loop(); i++ {
		id := fmt.Sprintf("call-%d", i)
		state.Ingest([]message.Message{{Parts: []message.ContentPart{
			message.ToolCall{ID: id, Name: "shell", Input: `{"command":"go test ./..."}`, Finished: true},
			message.ToolResult{ToolCallID: id, Name: "shell", Content: content, Metadata: `{"exit_code":0}`},
		}}})
	}
}

// BenchmarkExecutionStateIngestHistory is the per-turn cost paid at the
// start of Run: the whole history replayed into a state that has seen
// every result in it already.
func BenchmarkExecutionStateIngestHistory(b *testing.B) {
	msgs := benchmarkExecutionHistory()
	state := newExecutionState("")
	state.Ingest(msgs)
	b.ReportAllocs()
	for b.Loop() {
		state.Ingest(msgs)
	}
}
