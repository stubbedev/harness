package lsp

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// SettleTimeout is the ceiling on how long a background wait gives a language
// server to finish reporting on a change. It is only reached by a server that
// keeps republishing; one that answers and goes quiet is waited out by the
// settle window instead, and one that says nothing at all gives up after the
// shorter first-change window inside [Client.WaitForDiagnostics].
const SettleTimeout = 5 * time.Second

// NotifyChangeAsync tells every running server that handles path that the file
// changed, and returns as soon as those one-way notifications are written.
//
// Waiting for the servers to answer happens in the background. An edit that
// blocks on a language server pays that server's analysis time on every single
// write, which on a large project is seconds per edit and is spent whether or
// not the file has anything wrong with it. The answer is not thrown away: it
// lands in the client's diagnostic map, and the next thing that reports — the
// next tool call, or the sweep before the next model step — picks it up.
//
// Servers that are not running yet are started in the background and not
// waited for either.
func (s *Manager) NotifyChangeAsync(ctx context.Context, path string) {
	s.NotifyChangesAsync(ctx, path)
}

// NotifyChangesAsync is [Manager.NotifyChangeAsync] for a change that spans
// several files, such as a rename. The whole set is announced under a single
// background wait: a rename across fifty files has one answer to wait for, not
// fifty, and each server is waited on once however many of its files were
// touched.
func (s *Manager) NotifyChangesAsync(ctx context.Context, paths ...string) {
	if s == nil {
		return
	}

	var notified []*Client
	seen := make(map[*Client]struct{})
	for _, path := range paths {
		if path == "" {
			continue
		}
		// Detached from the tool call's context: the call returns long
		// before a cold server finishes starting, and cancelling it then
		// would leave the server half-initialized.
		s.StartAsync(context.WithoutCancel(ctx), path)

		for client := range s.Clients().Seq() {
			if !client.HandlesFile(path) {
				continue
			}
			if err := client.OpenFileOnDemand(ctx, path); err != nil {
				slog.Debug("Failed to open file for LSP notification", "path", path, "error", err)
				continue
			}
			if err := client.NotifyChange(ctx, path); err != nil {
				slog.Debug("Failed to notify LSP of file change", "path", path, "error", err)
				continue
			}
			if _, already := seen[client]; !already {
				seen[client] = struct{}{}
				notified = append(notified, client)
			}
		}
	}
	s.settleInBackground(ctx, notified)
}

// NotifyWorkspaceChangeAsync re-notifies every server about every file it has
// open and tells it the workspace changed, then returns. Use it after a change
// that reaches files the caller cannot enumerate cheaply.
//
// Like [Manager.NotifyChangeAsync], the wait for servers to settle runs in the
// background.
func (s *Manager) NotifyWorkspaceChangeAsync(ctx context.Context) {
	if s == nil {
		return
	}
	var notified []*Client
	for client := range s.Clients().Seq() {
		client.RefreshOpenFiles(ctx)
		if err := client.NotifyWorkspaceChange(ctx); err != nil {
			slog.Warn("Failed to notify workspace change", "error", err)
		}
		notified = append(notified, client)
	}
	s.settleInBackground(ctx, notified)
}

// settleInBackground waits for each client to stop republishing, off the
// caller's goroutine, and registers the wait so [Manager.AwaitSettled] can
// find it.
func (s *Manager) settleInBackground(ctx context.Context, clients []*Client) {
	if len(clients) == 0 {
		return
	}

	done := make(chan struct{})
	s.settleMu.Lock()
	s.settlePending = append(s.settlePending, done)
	s.settleMu.Unlock()

	// Detached: the tool call that triggered the change returns immediately,
	// and its context goes with it. The wait must outlive it or it would be
	// cancelled before any server has said anything.
	waitCtx := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			s.settleMu.Lock()
			s.settlePending = deleteChan(s.settlePending, done)
			s.settleMu.Unlock()
			close(done)
		}()
		var wg sync.WaitGroup
		for _, client := range clients {
			wg.Go(func() {
				client.WaitForDiagnostics(waitCtx, SettleTimeout)
			})
		}
		wg.Wait()
	}()
}

// AwaitSettled waits up to budget for the background settle waits that are
// already in flight. It is the small grace period a reporter gives a fast
// server so a change made moments ago is described in this report rather than
// the next one; a slow server simply misses the budget and is reported later.
func (s *Manager) AwaitSettled(ctx context.Context, budget time.Duration) {
	if s == nil || budget <= 0 {
		return
	}
	s.settleMu.Lock()
	pending := make([]chan struct{}, len(s.settlePending))
	copy(pending, s.settlePending)
	s.settleMu.Unlock()
	if len(pending) == 0 {
		return
	}

	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	for _, done := range pending {
		select {
		case <-done:
		case <-deadline.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Settling reports whether a server is still answering for a change. A report
// taken mid-flight is not just incomplete but wrong: servers clear a file's
// diagnostics before republishing them, so a snapshot taken in that gap reads
// as though the problems were fixed.
func (s *Manager) Settling() bool {
	if s == nil {
		return false
	}
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	return len(s.settlePending) > 0
}

func deleteChan(list []chan struct{}, target chan struct{}) []chan struct{} {
	for i, c := range list {
		if c == target {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}
