package dialog

import (
	"strings"
	"sync"

	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// questionDescCache caches markdown-rendered question descriptions.
// Descriptions are immutable once a question is asked, but Height and
// Draw each re-render them on every frame (spinner ticks, cursor
// blinks, typing), so caching by styles, width, and text avoids
// re-parsing markdown several times per frame per component.
var questionDescCache = struct {
	sync.Mutex
	m map[questionDescKey]string
}{m: map[questionDescKey]string{}}

type questionDescKey struct {
	sty   *styles.Styles
	width int
	text  string
}

// renderQuestionDescription renders a question description as
// markdown at the given width, with any trailing newline removed.
// Falls back to the raw text when rendering fails. Results are
// cached, so repeated calls with the same inputs are cheap.
func renderQuestionDescription(sty *styles.Styles, text string, width int) string {
	if text == "" {
		return ""
	}
	key := questionDescKey{sty: sty, width: width, text: text}
	questionDescCache.Lock()
	cached, ok := questionDescCache.m[key]
	questionDescCache.Unlock()
	if ok {
		return cached
	}
	r := common.MarkdownRenderer(sty, width)
	mu := common.LockMarkdownRenderer(r)
	mu.Lock()
	out, err := r.Render(text)
	mu.Unlock()
	if err != nil {
		return text
	}
	out = strings.TrimSuffix(out, "\n")
	questionDescCache.Lock()
	questionDescCache.m[key] = out
	questionDescCache.Unlock()
	return out
}
