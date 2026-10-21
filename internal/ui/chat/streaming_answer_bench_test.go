package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func BenchmarkAssistantRenderStreamingAnswer(b *testing.B) {
	sty := styles.CharmtonePantera()
	const width = 80
	var base strings.Builder
	for i := range 300 {
		fmt.Fprintf(&base, "Paragraph %d explains one more part of the answer in a sentence or two of prose.\n\n", i)
	}
	msg := &message.Message{ID: "bench", Role: message.Assistant}
	msg.AppendContent(base.String())
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	item.SetMessage(msg)
	_ = item.Render(width)

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		msg.AppendContent(fmt.Sprintf("tok%d ", i))
		if i%20 == 19 {
			msg.AppendContent("\n\n")
		}
		item.SetMessage(msg)
		_ = item.Render(width)
		i++
	}
}
