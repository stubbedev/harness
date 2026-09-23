package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/stubbedev/harness/internal/session"
)

// resolveSessionPrefix resolves id to a single session, accepting either
// a full session ID or a prefix of the hash `harness session list`
// prints. get and list reach whichever session source the caller has (a
// local service, a workspace or a client), and idOf reads the ID back
// out of its session type.
//
// describe, when non-nil, renders one line per candidate so an
// ambiguous prefix lists its matches the way Git does; nil reports only
// the match count.
func resolveSessionPrefix[T any](
	ctx context.Context,
	id string,
	get func(context.Context, string) (T, error),
	list func(context.Context) ([]T, error),
	idOf func(T) string,
	describe func(T) string,
) (T, error) {
	var zero T
	if s, err := get(ctx, id); err == nil {
		return s, nil
	}

	sessions, err := list(ctx)
	if err != nil {
		return zero, err
	}

	matches := session.FilterHashPrefix(sessions, id, idOf)
	switch {
	case len(matches) == 0:
		return zero, fmt.Errorf("session not found: %s", id)
	case len(matches) == 1:
		return matches[0], nil
	case describe == nil:
		return zero, fmt.Errorf("session ID %q is ambiguous (%d matches)", id, len(matches))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "session ID '%s' is ambiguous. Matches:\n\n", id)
	for _, m := range matches {
		sb.WriteString("  " + describe(m) + "\n")
	}
	sb.WriteString("\nUse more characters or the full hash")
	return zero, errors.New(sb.String())
}
