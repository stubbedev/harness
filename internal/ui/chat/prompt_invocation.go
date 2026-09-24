package chat

import (
	"fmt"
	"strings"

	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// promptInvocationRow renders the compact transcript row for a named
// prompt invocation - "Ran Prompt → name" - in the same resource style
// as the loaded-skill indicator, so an invoked prompt reads like a tool
// call rather than a typed user message.
func promptInvocationRow(sty *styles.Styles, name string) string {
	return sty.Tool.Body.Render(fmt.Sprintf(
		"%s %s %s",
		sty.Tool.ResourceLoadedText.Render("Ran Prompt"),
		sty.Tool.ResourceLoadedIndicator.Render(styles.ArrowRightIcon),
		sty.Tool.ResourceName.Render(name),
	))
}

// renderUserMarkdown renders text through the shared user-message
// markdown renderer, falling back to the raw text when rendering
// fails. Callers own the width.
func renderUserMarkdown(sty *styles.Styles, text string, width int) string {
	renderer := common.UserMarkdownRenderer(sty, width)
	mu := common.LockMarkdownRenderer(renderer)

	mu.Lock()
	result, err := renderer.Render(text)
	mu.Unlock()

	if err != nil {
		return text
	}
	return strings.TrimSuffix(result, "\n")
}
