package message

import (
	"strings"
	"testing"
)

// streamShape approximates one assistant turn: deltaLen bytes of text per
// provider delta, deltas of them.
type streamShape struct {
	name   string
	deltas int
	delta  string
}

var streamShapes = []streamShape{
	{"short_2KB", 500, "abcd"},
	{"medium_20KB", 5000, "abcd"},
	{"long_100KB", 25000, "abcd"},
}

func BenchmarkAppendContent(b *testing.B) {
	for _, shape := range streamShapes {
		b.Run(shape.name, func(b *testing.B) {
			for b.Loop() {
				msg := &Message{Role: Assistant}
				for range shape.deltas {
					msg.AppendContent(shape.delta)
				}
			}
		})
	}
}

func BenchmarkAppendReasoningContent(b *testing.B) {
	for _, shape := range streamShapes {
		b.Run(shape.name, func(b *testing.B) {
			for b.Loop() {
				msg := &Message{Role: Assistant}
				for range shape.deltas {
					msg.AppendReasoningContent(shape.delta)
				}
			}
		})
	}
}

// BenchmarkAppendBuilderBaseline is the same byte volume through a
// strings.Builder: the floor an amortized implementation could reach.
func BenchmarkAppendBuilderBaseline(b *testing.B) {
	for _, shape := range streamShapes {
		b.Run(shape.name, func(b *testing.B) {
			for b.Loop() {
				var sb strings.Builder
				for range shape.deltas {
					sb.WriteString(shape.delta)
				}
				_ = sb.String()
			}
		})
	}
}
