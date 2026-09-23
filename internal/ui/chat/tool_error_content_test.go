package chat

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// A canceled result is a deliberate stop and renders as a warning whatever
// its text says; any other error renders as an error.
func TestToolErrorContentUsesCanceledFlag(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()

	canceled := ansi.Strip(toolErrorContent(&sty, &message.ToolResult{
		Content:  "Error: user cancelled assistant tool calling",
		IsError:  true,
		Canceled: true,
	}, 80))
	require.Contains(t, canceled, "WARN")

	failed := ansi.Strip(toolErrorContent(&sty, &message.ToolResult{
		Content: "User cancelled",
		IsError: true,
	}, 80))
	require.Contains(t, failed, "ERROR")
}
