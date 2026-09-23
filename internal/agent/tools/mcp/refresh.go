package mcp

import (
	"context"
	"log/slog"
)

// refreshListing re-lists one kind of capability on the registered session,
// stores it, and republishes the server's counts. It runs under the renewal
// lock so the session cannot be swapped between the lookup and the state
// update; a stale error transition would otherwise tear down a healthy
// replacement.
func refreshListing[T any](
	ctx context.Context,
	name, kind string,
	list func(context.Context, *ClientSession) ([]T, error),
	store func([]T) int,
	setCount func(*Counts, int),
) {
	mu := renewLock(name)
	mu.Lock()
	defer mu.Unlock()

	session, ok := sessions.Get(name)
	if !ok {
		slog.Warn("MCP refresh found no session", "name", name, "kind", kind)
		return
	}

	items, err := list(ctx, session)
	if err != nil {
		updateState(name, StateError, err, session, Counts{})
		return
	}

	prev, _ := states.Get(name)
	setCount(&prev.Counts, store(items))
	updateState(name, StateConnected, nil, session, prev.Counts)
}
