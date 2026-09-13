package model

import (
	"strings"
	"testing"

	"github.com/stubbedev/harness/internal/session"
)

// roundedBorderRunes are chars that only appear when a pill has a visible
// rounded border.
const roundedBorderRunes = "╭╮╰╯"

func hasRoundedBorder(s string) bool {
	return strings.ContainsAny(s, roundedBorderRunes)
}

// todoPillHasBorder reports whether the To-Do pill is wrapped in a
// rounded border by checking the line directly above the To-Do label for
// a top border corner.
func todoPillHasBorder(view string) bool {
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "To-Do") {
			continue
		}
		if i == 0 {
			return false
		}
		return strings.ContainsAny(lines[i-1], "╭╮")
	}
	return false
}

// TestTodoPillAlwaysHasBorder guards CHARM-1678: the To-Do pill must
// render with its rounded border regardless of panel expansion.
func TestTodoPillAlwaysHasBorder(t *testing.T) {
	incompleteTodos := []session.Todo{{Content: "a", Status: session.TodoStatusPending}}

	cases := []struct {
		name     string
		expanded bool
		todos    []session.Todo
	}{
		{"collapsed", false, incompleteTodos},
		{"expanded", true, incompleteTodos},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newTestUI()
			u.session = &session.Session{ID: "s1", Todos: tc.todos}
			u.pillsExpanded = tc.expanded
			u.updateLayoutAndSize()
			u.renderPills()

			if !hasRoundedBorder(u.pillsView) {
				t.Fatalf("expected a rounded border somewhere in pills view:\n%s", u.pillsView)
			}
			if !todoPillHasBorder(u.pillsView) {
				t.Fatalf("expected the todo pill to have a border:\n%s", u.pillsView)
			}
		})
	}
}

// TestPillsViewOmitsQueue ensures queued prompts never render in the
// pills panel: they live in the transcript only.
func TestPillsViewOmitsQueue(t *testing.T) {
	u := newTestUI()
	u.session = &session.Session{ID: "s1", Todos: []session.Todo{
		{Content: "a", Status: session.TodoStatusPending},
	}}
	u.promptQueue = 2
	u.pillsExpanded = true
	u.updateLayoutAndSize()
	u.renderPills()

	if strings.Contains(u.pillsView, "Queued") {
		t.Fatalf("pills view must not mention the queue:\n%s", u.pillsView)
	}
	if !todoPillHasBorder(u.pillsView) {
		t.Fatalf("expected the todo pill to render:\n%s", u.pillsView)
	}
}
