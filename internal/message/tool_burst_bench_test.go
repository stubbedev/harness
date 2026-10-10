package message

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/session"
)

// benchService opens a message service on a fresh on-disk database,
// wired the way the app wires it, and creates a session to write into.
func benchService(b *testing.B) (Service, string) {
	b.Helper()
	conn, err := db.Connect(b.Context(), b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sess, err := session.NewService(q, conn).Create(b.Context(), "bench")
	if err != nil {
		b.Fatal(err)
	}
	return NewService(q), sess.ID
}

// BenchmarkToolCallBurst is what a step pays once the model has finished
// streaming a batch of tool calls: fantasy reports each call through
// OnToolCall, which stores it on the assistant message, and only then
// dispatches the batch. burst-ms is the time until the first tool may
// start; first-result-ms adds the first tool result row, which is where
// the assistant message must be on disk at the latest.
func BenchmarkToolCallBurst(b *testing.B) {
	for _, n := range []int{1, 5, 20} {
		b.Run(fmt.Sprintf("calls=%d", n), func(b *testing.B) {
			svc, sessionID := benchService(b)
			input := `{"content":"` + strings.Repeat("x", 32<<10) + `"}`

			var burst, first time.Duration
			for b.Loop() {
				msg, err := svc.Create(b.Context(), sessionID, CreateMessageParams{Role: Assistant})
				if err != nil {
					b.Fatal(err)
				}
				start := time.Now()
				for i := range n {
					msg.AddToolCall(ToolCall{ID: fmt.Sprintf("tc%d", i), Name: "write", Input: input, Finished: true})
					if err := svc.UpdateBuffered(b.Context(), msg); err != nil {
						b.Fatal(err)
					}
				}
				burst += time.Since(start)
				if _, err := svc.Create(b.Context(), sessionID, CreateMessageParams{
					Role:  Tool,
					Parts: []ContentPart{ToolResult{ToolCallID: "tc0", Name: "write", Content: "ok"}},
				}); err != nil {
					b.Fatal(err)
				}
				first += time.Since(start)
			}
			b.ReportMetric(float64(burst.Microseconds())/1000/float64(b.N), "burst-ms")
			b.ReportMetric(float64(first.Microseconds())/1000/float64(b.N), "first-result-ms")
		})
	}
}

// BenchmarkParallelToolResults stores a batch of 32 KB tool results from
// as many goroutines at once, the way parallel tools report back.
func BenchmarkParallelToolResults(b *testing.B) {
	for _, n := range []int{1, 20} {
		b.Run(fmt.Sprintf("results=%d", n), func(b *testing.B) {
			svc, sessionID := benchService(b)
			content := strings.Repeat("y", 32<<10)
			for b.Loop() {
				var wg sync.WaitGroup
				for i := range n {
					wg.Go(func() {
						if _, err := svc.Create(b.Context(), sessionID, CreateMessageParams{
							Role:  Tool,
							Parts: []ContentPart{ToolResult{ToolCallID: fmt.Sprintf("tc%d", i), Name: "view", Content: content}},
						}); err != nil {
							b.Error(err)
						}
					})
				}
				wg.Wait()
			}
		})
	}
}

// BenchmarkReadDuringToolResults reads one message while 20 goroutines
// keep storing 32 KB tool results: a read queued behind the writes
// waits for them.
func BenchmarkReadDuringToolResults(b *testing.B) {
	svc, sessionID := benchService(b)
	probe, err := svc.Create(b.Context(), sessionID, CreateMessageParams{Role: User, Parts: []ContentPart{TextContent{Text: "probe"}}})
	if err != nil {
		b.Fatal(err)
	}
	content := strings.Repeat("y", 32<<10)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			for ctx.Err() == nil {
				_, _ = svc.Create(ctx, sessionID, CreateMessageParams{
					Role:  Tool,
					Parts: []ContentPart{ToolResult{ToolCallID: fmt.Sprintf("tc%d", i), Name: "view", Content: content}},
				})
			}
		})
	}
	for b.Loop() {
		if _, err := svc.Get(b.Context(), probe.ID); err != nil {
			b.Fatal(err)
		}
	}
	cancel()
	wg.Wait()
}
