// Package agent is the core orchestration layer for Harness AI agents.
//
// It provides session-based AI agent functionality for managing
// conversations, tool execution, and message handling. It coordinates
// interactions between language models, messages, sessions, and tools while
// handling features like automatic summarization, queuing, and token
// management.
package agent

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/crash"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/hooks"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/presence"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/stringext"
	"github.com/stubbedev/harness/internal/verification"
	"github.com/stubbedev/harness/internal/version"
)

const (
	DefaultSessionName = "Untitled Session"

	// Constants for auto-summarization thresholds
	largeContextWindowThreshold = 200_000
	largeContextWindowBuffer    = 20_000
	smallContextWindowRatio     = 0.2
)

// autoSummarizeThreshold returns how many tokens may remain in a context
// window of cw tokens before the session is summarized. Windows above
// largeContextWindowThreshold keep a flat buffer, smaller ones reserve a
// share of the window. A configured buffer or ratio only replaces the
// default of its own regime.
func autoSummarizeThreshold(cw int64, ratio float64, buffer int64) int64 {
	if cw > largeContextWindowThreshold {
		if buffer > 0 {
			return buffer
		}
		return largeContextWindowBuffer
	}
	if ratio > 0 {
		return int64(float64(cw) * ratio)
	}
	return int64(float64(cw) * smallContextWindowRatio)
}

var userAgent = fmt.Sprintf("Harness/%s (https://github.com/stubbedev/harness)", version.Version)

//go:embed templates/title.md
var titlePrompt []byte

//go:embed templates/summary.md
var summaryPrompt []byte

// Used to remove <think> tags from generated titles.
var (
	thinkTagRegex       = regexp.MustCompile(`(?s)<think>.*?</think>`)
	orphanThinkTagRegex = regexp.MustCompile(`</?think>`)
)

type SessionAgentCall struct {
	SessionID string
	// RunID, when non-empty, is the caller-supplied correlator that
	// gets echoed back on the notify.RunComplete event emitted for
	// this turn. It is preserved when the call is enqueued behind a
	// busy session so the queued turn's terminal event is still
	// recognisable to the original caller. Callers that need a
	// reliable completion contract (e.g. `harness run` against a
	// session that may be busy) MUST set it; SessionID alone is
	// ambiguous when concurrent turns share the same session.
	RunID            string
	Prompt           string
	ProviderOptions  fantasy.ProviderOptions
	Attachments      []message.Attachment
	MaxOutputTokens  int64
	Temperature      *float64
	TopP             *float64
	TopK             *int64
	FrequencyPenalty *float64
	PresencePenalty  *float64
	NonInteractive   bool
	// OverflowRecovered marks a call that has already been summarized and
	// requeued once after a context-window overflow, so the recovery path
	// does not loop on a session that still exceeds the window after
	// summarizing.
	OverflowRecovered bool
	// OnComplete, when non-nil, replaces the default RunComplete
	// publish path: the inner Run hands the terminal payload to this
	// callback instead of emitting it on the RunComplete broker. The
	// coordinator uses this hook to coalesce the unauthorized →
	// re-auth → retry chain into a single user-visible terminal
	// event, so non-interactive clients (e.g. `harness run`) don't
	// exit on a stale failed-attempt RunComplete before the
	// successful retry. It is intentionally stripped when queueing
	// a busy-session call (see Run): the originating
	// coordinator.Run has long returned by the time the queued
	// recursion drains, so falling back to the default broker
	// publish keeps the event visible to subscribers.
	OnComplete func(notify.RunComplete)
	// Accepted, when non-nil, is the accept reservation taken by
	// BeginAccepted before the call was dispatched onto a goroutine
	// (the client/server fire-and-forget path). Run consumes it under
	// dispatchMu[SessionID] once the accepted -> (cancel-on-entry |
	// queued | active) transition has been chosen. When nil
	// (in-process / local callers like AppWorkspace), behavior is
	// unchanged and no accept tracking applies.
	Accepted *AcceptedRun
	// acceptSeq carries the accept sequence of the handle that produced
	// this call after it has been enqueued and its Accepted handle
	// stripped. The queue-drain paths compare it against a session's
	// cancel mark so a follow-up queued before a cancel is dropped while
	// one queued after the cancel survives. 0 means untracked (an
	// in-process enqueue with no accept reservation), which the drain
	// paths treat as covered by any present mark, preserving the
	// pre-sequence behavior.
	acceptSeq uint64
	// OnAuthRefresh, when non-nil, is called by fantasy when a stream
	// fails with an authentication error (HTTP 401). The callback should
	// refresh credentials and return nil on success, in which case
	// fantasy retries the stream transparently. Returning an error
	// surfaces the original auth error without retry.
	OnAuthRefresh func(ctx context.Context, err *fantasy.ProviderError) error
}

type SessionAgent interface {
	Run(context.Context, SessionAgentCall) (*fantasy.AgentResult, error)
	BeginAccepted(sessionID string) *AcceptedRun
	SetModels(large Model, small Model)
	SetTools(tools []fantasy.AgentTool)
	SetSystemPrompt(systemPrompt string)
	Cancel(sessionID string)
	// CancelTurn interrupts the session's active run only; queued
	// prompts and accepted runs survive it.
	CancelTurn(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	ClearQueue(sessionID string)
	// Summarize compacts the session. instructions optionally steers
	// what the summary focuses on (manual /compact input).
	Summarize(context.Context, string, fantasy.ProviderOptions, func(context.Context, *fantasy.ProviderError) error, string) error
	Model() Model
	GenerateTitle(ctx context.Context, sessionID, userPrompt string)
}

type Model struct {
	Model      fantasy.LanguageModel
	CatalogCfg catalog.Model
	ModelCfg   config.SelectedModel
	FlatRate   bool
}

// activeCancel wraps a context.CancelFunc with a unique pointer identity.
// The pointer is used for compare-and-delete in the dispatch completion path:
// when a finishing run's deferred cleanup fires, it must only remove its own
// entry — not a newer run's entry that was installed in the window between
// the explicit Del and the function return.
type activeCancel struct {
	cancel context.CancelFunc
}

type sessionAgent struct {
	cfg                   *config.ConfigStore
	largeModel            *csync.Value[Model]
	smallModel            *csync.Value[Model]
	systemPromptPrefix    *csync.Value[string]
	systemPrompt          *csync.Value[string]
	tools                 *csync.Slice[fantasy.AgentTool]
	skillActivation       *SkillActivationConfig
	directoryInstructions *DirectoryInstructions

	isSubAgent           bool
	sessions             session.Service
	messages             message.Service
	files                history.Service
	checkpoints          *checkpoints.Service
	disableAutoSummarize bool
	autoSummarizeRatio   float64
	autoSummarizeBuffer  int64
	maxRetries           *int
	notify               pubsub.Publisher[notify.Notification]
	runComplete          pubsub.Publisher[notify.RunComplete]

	// lspManager is consulted before each step for diagnostics the
	// language servers have produced since the last one. Writes hand their
	// file over without waiting, so this is where a slow server finally
	// gets heard. Nil when no LSP is configured.
	lspManager *lsp.Manager

	// hooks fires user-configured hook events for this agent's runs.
	// Sub-agents hold the registry too, but the prompt/turn/compact
	// events are gated on isSubAgent so only the top-level agent fires
	// them; SubagentStop fires from the coordinator's dispatch path.
	hooks *hooks.Registry

	// subagentInbox, when set, supplies messages background sub-agents
	// have sent this session. PrepareStep drains it so a message from a
	// running child lands in its dispatcher's next step.
	subagentInbox SubagentInboxSource

	// queueNotify, when set, is told every time a prompt is queued for a
	// busy session (see QueueArrivalNotifier).
	queueNotify QueueArrivalNotifier

	// presence publishes this instance's liveness, session and recent
	// file activity to every other harness instance working in the same
	// workspace, and feeds the concurrent-instance context note. Nil for
	// sub-agents and in tests.
	presence *presence.Registry

	messageQueue   *csync.Map[string, []SessionAgentCall]
	activeRequests *csync.Map[string, *activeCancel]

	// dispatchMu holds a per-session mutex that serializes the
	// accepted -> (cancel-on-entry | queued | active) transition in
	// Run against a concurrent Cancel. The lock is held only during
	// the brief handoff (no DB or LLM I/O under the lock).
	dispatchMu *csync.Map[string, *sync.Mutex]
	// acceptedRuns counts dispatched-but-not-yet-active runs per
	// session. A counter > 0 means a dispatched prompt is in flight
	// and has not yet completed the dispatch handoff in Run. Only
	// BeginAccepted increments it; only AcceptedRun.Close decrements
	// it.
	acceptedRuns *csync.Map[string, int]
	// cancelMark records, per session, a high-water accept sequence: an
	// accepted handle is canceled by it iff the handle's sequence is at
	// or below the mark. Cancel raises the mark to the latest sequence
	// assigned at cancel time, so a single Cancel covers every prompt
	// accepted-but-not-yet-active then, while a prompt accepted later
	// (higher sequence) is never poisoned. Absent or 0 means no pending
	// cancel. It is only raised by Cancel when acceptedRuns > 0, so an
	// idle Escape never records a mark.
	cancelMark *csync.Map[string, uint64]
	// dispatchMuCreate guards lazy creation of per-session entries in
	// dispatchMu so two goroutines can't race to lock different mutex
	// instances for the same session.
	dispatchMuCreate sync.Mutex
	// acceptedMu serializes increments/decrements of acceptedRuns and
	// the assignment of accept sequence numbers from acceptSeqGen. It
	// is separate from dispatchMu so AcceptedRun.Close (which may run
	// while Run holds dispatchMu for the same session) does not
	// deadlock by re-entering the dispatch lock.
	acceptedMu sync.Mutex
	// acceptSeqGen is the monotonic source of accept sequence numbers.
	// Each BeginAccepted increments it under acceptedMu and stamps the
	// returned handle, so sequences strictly increase in accept order
	// across the agent. Cancel uses its current value as the per-session
	// high-water mark.
	acceptSeqGen uint64
}

type SessionAgentOptions struct {
	Config                *config.ConfigStore
	LargeModel            Model
	SmallModel            Model
	SystemPromptPrefix    string
	SystemPrompt          string
	SkillActivation       *SkillActivationConfig
	DirectoryInstructions *DirectoryInstructions
	IsSubAgent            bool
	DisableAutoSummarize  bool
	AutoSummarizeRatio    float64
	AutoSummarizeBuffer   int64
	MaxRetries            *int
	Sessions              session.Service
	Messages              message.Service
	Files                 history.Service
	// LSPManager supplies the diagnostics swept into each step. Nil
	// disables the sweep.
	LSPManager *lsp.Manager
	// Checkpoints snapshots the working tree at each user turn so
	// the session can be rewound. Nil disables checkpoints. It is
	// ignored for sub-agents: only the top-level coder agent records
	// rewind points.
	Checkpoints *checkpoints.Service
	Tools       []fantasy.AgentTool
	Notify      pubsub.Publisher[notify.Notification]
	RunComplete pubsub.Publisher[notify.RunComplete]
	Hooks       *hooks.Registry
	// SubagentInbox, when set, is drained per step so messages from
	// running background sub-agents reach this session mid-turn.
	SubagentInbox SubagentInboxSource
	// QueueNotify, when set, is told every time a prompt is queued for
	// a busy session, so a wait blocked on that session can surface it
	// at once instead of sleeping to its timeout.
	QueueNotify QueueArrivalNotifier
	// Presence publishes this instance in the workspace's cross-process
	// registry and supplies the concurrent-instance context note. Nil
	// (sub-agents, tests) disables both directions.
	Presence *presence.Registry
}

func NewSessionAgent(
	opts SessionAgentOptions,
) SessionAgent {
	return &sessionAgent{
		cfg:                   opts.Config,
		largeModel:            csync.NewValue(opts.LargeModel),
		smallModel:            csync.NewValue(opts.SmallModel),
		systemPromptPrefix:    csync.NewValue(opts.SystemPromptPrefix),
		systemPrompt:          csync.NewValue(opts.SystemPrompt),
		skillActivation:       opts.SkillActivation,
		directoryInstructions: opts.DirectoryInstructions,
		isSubAgent:            opts.IsSubAgent,
		sessions:              opts.Sessions,
		messages:              opts.Messages,
		files:                 opts.Files,
		lspManager:            opts.LSPManager,
		checkpoints:           opts.Checkpoints,
		disableAutoSummarize:  opts.DisableAutoSummarize,
		autoSummarizeRatio:    opts.AutoSummarizeRatio,
		autoSummarizeBuffer:   opts.AutoSummarizeBuffer,
		maxRetries:            opts.MaxRetries,
		tools:                 csync.NewSliceFrom(withResultCap(opts.Tools)),
		notify:                opts.Notify,
		runComplete:           opts.RunComplete,
		hooks:                 opts.Hooks,
		subagentInbox:         opts.SubagentInbox,
		queueNotify:           opts.QueueNotify,
		presence:              opts.Presence,
		messageQueue:          csync.NewMap[string, []SessionAgentCall](),
		activeRequests:        csync.NewMap[string, *activeCancel](),
		dispatchMu:            csync.NewMap[string, *sync.Mutex](),
		acceptedRuns:          csync.NewMap[string, int](),
		cancelMark:            csync.NewMap[string, uint64](),
	}
}

func (a *sessionAgent) retryOption() fantasy.AgentOption {
	maxRetries := fantasy.DefaultRetryOptions().MaxRetries
	if a.maxRetries != nil {
		maxRetries = *a.maxRetries
	}
	return fantasy.WithMaxRetries(maxRetries)
}

// AcceptedRun owns exactly one accept reservation taken by
// BeginAccepted. It is the only carrier of accept-state across the
// backend.runAgent / Coordinator.Run / sessionAgent.Run layers: a
// counter > 0 means a dispatched prompt is in flight and has not yet
// completed the dispatch handoff in Run. Close is the only way to
// release the reservation and is idempotent.
type AcceptedRun struct {
	agent     *sessionAgent
	sessionID string
	// seq is the monotonic accept sequence stamped by BeginAccepted. A
	// cancel covers this handle iff seq is at or below the session's
	// cancel mark, so a handle accepted after a cancel (higher seq) is
	// never poisoned by it.
	seq  uint64
	done atomic.Bool
}

// Close decrements the accept counter for this reservation. It is safe
// to call multiple times; only the first call has effect.
func (r *AcceptedRun) Close() {
	if r == nil {
		return
	}
	if !r.done.CompareAndSwap(false, true) {
		return
	}
	r.agent.endAccepted(r.sessionID)
}

// SessionID exposes the session this reservation is for so the run path
// can use it without an extra parameter.
func (r *AcceptedRun) SessionID() string {
	if r == nil {
		return ""
	}
	return r.sessionID
}

// BeginAccepted increments the accept counter for sessionID and returns
// a handle whose Close is the only way to decrement it. It is the only
// entry point that mutates acceptedRuns.
func (a *sessionAgent) BeginAccepted(sessionID string) *AcceptedRun {
	a.acceptedMu.Lock()
	defer a.acceptedMu.Unlock()
	count, _ := a.acceptedRuns.Get(sessionID)
	a.acceptedRuns.Set(sessionID, count+1)
	a.acceptSeqGen++
	return &AcceptedRun{agent: a, sessionID: sessionID, seq: a.acceptSeqGen}
}

// endAccepted decrements the accept counter for sessionID. It is only
// called via AcceptedRun.Close. It uses a dedicated lock (not the
// per-session dispatch mutex) so it can run while Run holds dispatchMu
// for the same session without deadlocking.
//
// When the count reaches zero the session's cancel mark is dropped: no
// accepted handle remains for it to cover, and any handle accepted later
// gets a strictly higher sequence that the mark would not match anyway.
// Handles canceled on entry never reach RunComplete, so this is the only
// place that clears the mark for an all-canceled batch. Sibling handles
// covered by the same mark are serialized on the per-session dispatch
// mutex and read the mark before they Close, so this never clears it out
// from under a covered handle still waiting to enter Run.
func (a *sessionAgent) endAccepted(sessionID string) {
	a.acceptedMu.Lock()
	defer a.acceptedMu.Unlock()
	count, ok := a.acceptedRuns.Get(sessionID)
	if !ok || count <= 1 {
		a.acceptedRuns.Del(sessionID)
		a.cancelMark.Del(sessionID)
		return
	}
	a.acceptedRuns.Set(sessionID, count-1)
}

// sessionMu returns the per-session dispatch mutex, creating it on first
// use. Creation is guarded so concurrent callers always observe the same
// mutex instance for a given session.
func (a *sessionAgent) sessionMu(sessionID string) *sync.Mutex {
	if mu, ok := a.dispatchMu.Get(sessionID); ok {
		return mu
	}
	a.dispatchMuCreate.Lock()
	defer a.dispatchMuCreate.Unlock()
	if mu, ok := a.dispatchMu.Get(sessionID); ok {
		return mu
	}
	mu := &sync.Mutex{}
	a.dispatchMu.Set(sessionID, mu)
	return mu
}

// enqueueCall appends call to the session's message queue. The
// OnComplete hook is stripped: the caller that supplied it (typically
// coordinator.Run) has its own retry/coalesce scope that ends when it
// returns, so by the time the queue drains nobody is left to consume the
// buffered terminal event. The recursive Run falls back to the default
// broker publish, which is what existing subscribers expect for queued
// turns.
func (a *sessionAgent) enqueueCall(call SessionAgentCall) {
	existing, ok := a.messageQueue.Get(call.SessionID)
	if !ok {
		existing = []SessionAgentCall{}
	}
	queued := call
	if call.Accepted != nil {
		// Preserve the accept sequence after the handle is stripped so
		// the queue-drain paths can tell a follow-up queued before a
		// cancel (covered by the mark) from one queued after it.
		queued.acceptSeq = call.Accepted.seq
	}
	queued.OnComplete = nil
	queued.Accepted = nil
	existing = append(existing, queued)
	a.messageQueue.Set(call.SessionID, existing)
	if a.queueNotify != nil {
		a.queueNotify.NotifyQueueArrival(call.SessionID)
	}
}

// drainQueueForStep partitions the session's queued calls for the current
// streaming step under the per-session dispatch mutex so the filtering is
// atomic against a concurrent Cancel: canceledBySeq requires the caller to
// hold that mutex, and evaluating it here (rather than after unlocking)
// prevents a cancel recorded between the drain and the check from being
// observed inconsistently.
//
// Calls covered by a pending cancel are dropped; the dropped ones that
// carry a RunID are returned in canceledWithRunID so the caller can
// publish their terminal cancelled RunComplete (a caller waiting on that
// RunID, e.g. `harness run`, would otherwise hang). Uncanceled calls without
// a RunID are returned in fold to be folded into the active turn,
// preserving the existing follow-up behavior. Uncanceled calls that carry
// a RunID are left in the queue so each runs as its own turn via the
// recursive run path and publishes its own RunComplete, giving every
// RunID-bearing prompt an explicit lifecycle instead of being silently
// absorbed into another turn. fold is processed by the caller without the
// lock held.
func (a *sessionAgent) drainQueueForStep(sessionID string) (fold, canceledWithRunID []SessionAgentCall) {
	dispatchLock := a.sessionMu(sessionID)
	dispatchLock.Lock()
	defer dispatchLock.Unlock()
	queuedCalls, _ := a.messageQueue.Get(sessionID)
	var keep []SessionAgentCall
	for _, queued := range queuedCalls {
		if a.canceledBySeq(sessionID, queued.acceptSeq) {
			if queued.RunID != "" {
				canceledWithRunID = append(canceledWithRunID, queued)
			}
			continue
		}
		if queued.RunID != "" {
			keep = append(keep, queued)
			continue
		}
		fold = append(fold, queued)
	}
	if len(keep) == 0 {
		a.messageQueue.Del(sessionID)
	} else {
		a.messageQueue.Set(sessionID, keep)
	}
	return fold, canceledWithRunID
}

// requeueCalls returns calls to the front of the session's queue, under
// the per-session dispatch mutex, after a fold consumed them but could
// not create their user message (e.g. the step context was canceled
// mid-fold). Without this the drained prompts would be silently dropped.
func (a *sessionAgent) requeueCalls(sessionID string, calls []SessionAgentCall) {
	if len(calls) == 0 {
		return
	}
	dispatchLock := a.sessionMu(sessionID)
	dispatchLock.Lock()
	defer dispatchLock.Unlock()
	existing, _ := a.messageQueue.Get(sessionID)
	a.messageQueue.Set(sessionID, append(slices.Clone(calls), existing...))
}

// appendQueued adds calls to the back of the session's queue under the
// per-session dispatch mutex. Unlike enqueueCall it keeps the calls'
// OnComplete: it is for a run re-queueing its own call to continue after
// a compaction, which the same Run stack dequeues again, so the hook's
// owner is still waiting on it.
func (a *sessionAgent) appendQueued(sessionID string, calls ...SessionAgentCall) {
	dispatchLock := a.sessionMu(sessionID)
	dispatchLock.Lock()
	defer dispatchLock.Unlock()
	existing, _ := a.messageQueue.Get(sessionID)
	a.messageQueue.Set(sessionID, append(existing, calls...))
}

// dequeueNextLocked takes the next turn off the session's queue. The
// caller must hold the session's dispatch mutex.
//
// Queued calls covered by a pending cancel are dropped first; the dropped
// ones are returned in drops so the caller can publish their terminal
// cancelled RunComplete once it has released the lock. The leading run of
// calls without a RunID was queued as one steering burst and is joined
// into a single turn; RunID-bearing calls each keep their own turn. The
// dequeued call gets a fresh accept reservation so acceptedRuns stays
// above zero across the handoff into the Run that executes it: a cancel
// arriving in that window records a mark the Run observes as
// cancel-on-entry.
//
// remaining is the queue after the cancel filter, including the call
// being dequeued. When it is empty, ok is false and the cancel mark is
// cleared unless an accepted run it may still cover is in flight.
func (a *sessionAgent) dequeueNextLocked(sessionID string) (next SessionAgentCall, remaining, drops []SessionAgentCall, ok bool) {
	queued, _ := a.messageQueue.Get(sessionID)
	for _, q := range queued {
		if a.canceledBySeq(sessionID, q.acceptSeq) {
			drops = append(drops, q)
			continue
		}
		remaining = append(remaining, q)
	}
	if len(remaining) == 0 {
		a.messageQueue.Del(sessionID)
		a.acceptedMu.Lock()
		inFlight, _ := a.acceptedRuns.Get(sessionID)
		a.acceptedMu.Unlock()
		if inFlight == 0 {
			a.cancelMark.Del(sessionID)
		}
		return SessionAgentCall{}, nil, drops, false
	}
	n := 1
	if remaining[0].RunID == "" {
		for n < len(remaining) && remaining[n].RunID == "" {
			n++
		}
	}
	next = joinQueuedCalls(remaining[:n])
	a.messageQueue.Set(sessionID, slices.Clone(remaining[n:]))
	next.Accepted = a.BeginAccepted(sessionID)
	return next, remaining, drops, true
}

// joinQueuedCalls merges queued calls without a RunID into a single
// call: prompts joined with message.QueuedPromptSeparator and
// attachments concatenated, so a queue drained as one steering burst
// runs as a single user message. The first call's fields win; queued
// TUI prompts carry no sampling options of their own.
func joinQueuedCalls(calls []SessionAgentCall) SessionAgentCall {
	joined := calls[0]
	if len(calls) == 1 {
		return joined
	}
	prompts := make([]string, len(calls))
	for i, call := range calls {
		prompts[i] = call.Prompt
	}
	joined.Prompt = strings.Join(prompts, message.QueuedPromptSeparator)
	for _, call := range calls[1:] {
		joined.Attachments = append(joined.Attachments, call.Attachments...)
	}
	return joined
}

// publishCanceledQueueDrops emits a terminal cancelled RunComplete for
// every dropped queued call that carries a RunID. A queued prompt removed
// from the queue without ever running — covered by a pending cancel, or
// cleared by Cancel/ClearQueue — would otherwise leave a caller blocked on
// that RunID: `harness run` ignores live message events and exits only on a
// RunComplete whose RunID matches. Calls without a RunID had no such waiter
// and are dropped silently as before. A detached, bounded context keeps the
// must-deliver publish alive even when the run context that triggered the
// drop is already canceled.
func (a *sessionAgent) publishCanceledQueueDrops(drops []SessionAgentCall) {
	var hasRunID bool
	for _, d := range drops {
		if d.RunID != "" {
			hasRunID = true
			break
		}
	}
	if !hasRunID {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, d := range drops {
		if d.RunID == "" {
			continue
		}
		a.publishRunComplete(ctx, d, notify.RunComplete{
			SessionID: d.SessionID,
			RunID:     d.RunID,
			Cancelled: true,
		})
	}
}

// clearQueueAndNotify removes all queued prompts for the session and
// publishes a terminal cancelled RunComplete for any that carried a RunID,
// so callers waiting on those RunIDs (e.g. `harness run`) are not left
// hanging when their queued prompt is discarded without running.
func (a *sessionAgent) clearQueueAndNotify(sessionID string) {
	queued, ok := a.messageQueue.Take(sessionID)
	if !ok {
		return
	}
	a.publishCanceledQueueDrops(queued)
}

// clearPendingCancel removes any pending-cancel mark for sessionID. It
// takes the per-session dispatch lock so it is ordered against Cancel
// and the dispatch handoff.
func (a *sessionAgent) clearPendingCancel(sessionID string) {
	mu := a.sessionMu(sessionID)
	mu.Lock()
	defer mu.Unlock()
	a.cancelMark.Del(sessionID)
}

// canceledBySeq reports whether an accepted handle or queued call with
// the given accept sequence is covered by a pending cancel for the
// session. Callers must hold the session's dispatch mutex. A tracked
// sequence (seq > 0) is covered only when it is at or below the cancel
// high-water mark, so a prompt accepted after the cancel (higher seq) is
// never poisoned. An untracked sequence (seq == 0, an in-process enqueue
// with no accept reservation) is covered whenever any mark is present,
// preserving the pre-sequence behavior. The mark is not consumed: it
// stays so every sibling handle it covers observes the same cancel, and
// a later handle (higher seq) ignores it regardless.
func (a *sessionAgent) canceledBySeq(sessionID string, seq uint64) bool {
	mark, ok := a.cancelMark.Get(sessionID)
	if !ok || mark == 0 {
		return false
	}
	return seq == 0 || seq <= mark
}

// persistCanceledTurn writes the user/assistant records for a turn that
// was canceled before (or just as) streaming would have produced them.
// It creates the user message only when it was not already created by an
// earlier createUserMessage call (userMsgCreated), then writes an
// assistant message with FinishReasonCanceled. Both writes use
// context.WithoutCancel(ctx) so workspace shutdown (which cancels the run
// context) can't drop them.
func (a *sessionAgent) persistCanceledTurn(ctx context.Context, call SessionAgentCall, userMsgCreated bool) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if !userMsgCreated {
		if _, err := a.createUserMessage(writeCtx, call); err != nil {
			return err
		}
	}
	largeModel := a.largeModel.Get()
	assistant, err := a.messages.Create(writeCtx, call.SessionID, message.CreateMessageParams{
		Role:     message.Assistant,
		Parts:    []message.ContentPart{},
		Model:    largeModel.ModelCfg.Model,
		Provider: largeModel.ModelCfg.Provider,
	})
	if err != nil {
		return err
	}
	assistant.AddFinish(message.FinishReasonCanceled, "User canceled request", "")
	return a.messages.Update(writeCtx, assistant)
}

// publishRunComplete emits the authoritative terminal event for a turn.
// It honors the per-call OnComplete hook when set (so the coordinator can
// coalesce retries) and otherwise falls back to the RunComplete broker.
// ctx is used only for the bounded-blocking must-deliver publish; the
// terminal payload is supplied by the caller. This is the single emit path
// shared by the streaming defer and the cancel-on-entry early return so a
// caller waiting on RunComplete (e.g. `harness run` with a RunID) always
// observes exactly one terminal event regardless of which Run branch ends
// the turn.
func (a *sessionAgent) publishRunComplete(ctx context.Context, call SessionAgentCall, complete notify.RunComplete) {
	if call.OnComplete != nil {
		call.OnComplete(complete)
		return
	}
	if a.runComplete == nil {
		return
	}
	a.runComplete.PublishMustDeliver(ctx, pubsub.UpdatedEvent, complete)
}

// stepToolContext carries what tools read about the step they run in:
// the assistant message their results attach to and the model's image
// support and name.
func stepToolContext(ctx context.Context, assistantID string, model Model) context.Context {
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, assistantID)
	ctx = context.WithValue(ctx, tools.SupportsImagesContextKey, model.CatalogCfg.SupportsImages)
	ctx = context.WithValue(ctx, tools.ModelNameContextKey, model.CatalogCfg.Name)
	// A fresh shell session counter per step lets the shell tool spread
	// several parallel shell calls across distinct terminals while the
	// first keeps the stable default; see shellSessionSpreadTool.
	return withShellSessionCounter(ctx)
}

// turnComplete builds the terminal RunComplete for a turn that streamed:
// the assistant's ID and text when there is one, the error the turn ended
// with, and whether it was canceled — by that error, or by ctx.
func turnComplete(ctx context.Context, call SessionAgentCall, assistant *message.Message, err error) notify.RunComplete {
	complete := notify.RunComplete{SessionID: call.SessionID, RunID: call.RunID}
	if assistant != nil {
		complete.MessageID = assistant.ID
		complete.Text = assistant.Content().String()
	}
	if err != nil {
		complete.Error = err.Error()
		complete.Cancelled = errors.Is(err, context.Canceled)
	} else if ctx.Err() != nil {
		complete.Cancelled = true
	}
	return complete
}

// publishNotification fires Notification hooks for n and then publishes
// it to the notification broker. Sub-agents and agents without a
// publisher skip straight to the (no-op) publish.
func (a *sessionAgent) publishNotification(ctx context.Context, n notify.Notification) {
	if a.notify == nil {
		return
	}
	if !a.isSubAgent && a.hooks.Has(hooks.EventNotification) {
		if _, err := a.hooks.Run(ctx, hooks.EventContext{
			Event:            hooks.EventNotification,
			SessionID:        n.SessionID,
			NotificationType: string(n.Type),
			Message:          n.Message,
		}); err != nil {
			slog.Warn("Notification hook error", "error", err)
		}
	}
	a.notify.Publish(pubsub.CreatedEvent, n)
}

// fireStopHooks fires the Stop event after a completed top-level turn.
// The event is informational: hook decisions are logged, not enforced.
func (a *sessionAgent) fireStopHooks(ctx context.Context, sessionID string) {
	if a.isSubAgent || !a.hooks.Has(hooks.EventStop) {
		return
	}
	res, err := a.hooks.Run(ctx, hooks.EventContext{
		Event:     hooks.EventStop,
		SessionID: sessionID,
	})
	if err != nil {
		slog.Warn("Stop hook error", "error", err)
		return
	}
	if res.Context != "" {
		slog.Info("Stop hook context", "context", res.Context)
	}
}

// ValidateCall performs the cheap structural validation that
// sessionAgent.Run requires before a call can be dispatched: a call must
// carry either a non-empty prompt or a text attachment, and it must name a
// session. It is exported so callers that accept a run before dispatching it
// (e.g. backend.SendMessage) can apply the same checks and keep the error
// contract consistent.
func ValidateCall(call SessionAgentCall) error {
	if call.Prompt == "" && !message.ContainsTextAttachment(call.Attachments) {
		return ErrEmptyPrompt
	}
	if call.SessionID == "" {
		return ErrSessionMissing
	}
	return nil
}

// UUIDPattern matches the canonical UUID form every externally visible
// harness identifier is minted in: session IDs (see session.Create), run
// IDs, workspace IDs, and client IDs. Wire intake validates against it
// so an externally supplied identifier can never carry path or command
// metacharacters into the places an identifier reaches: scratch
// directories, context values, and child-process environments.
var UUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// OptionalUUIDPattern matches UUIDPattern or the empty string, for
// wire identifiers a caller may legitimately omit (an empty value means
// "not supplied").
var OptionalUUIDPattern = regexp.MustCompile(`^$|^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (a *sessionAgent) Run(ctx context.Context, call SessionAgentCall) (result *fantasy.AgentResult, retErr error) {
	if err := ValidateCall(call); err != nil {
		return nil, err
	}

	// genCtx/cancel are the run context and its cancel func, created under
	// the per-session dispatch mutex below so a concurrent Cancel can observe
	// the activeRequests entry before the assistant message exists.
	var (
		genCtx         context.Context
		cancel         context.CancelFunc
		userMsgCreated bool
	)

	// Serialize the dispatch decision (cancel-on-entry | queued | active)
	// against a concurrent Cancel. Cancel takes the same per-session lock, so
	// every cancel observes at least one of: a cancel mark, an activeRequests
	// entry, or a messageQueue entry it then clears. Holding the lock across
	// the busy check and the active registration also makes them atomic, so
	// two concurrent in-process callers — a burst of channel events, or a
	// channel event racing a typed prompt — cannot both pass the busy check
	// and start two runs on the same session.
	sessMu := a.sessionMu(call.SessionID)
	sessMu.Lock()

	if call.Accepted != nil && a.canceledBySeq(call.SessionID, call.Accepted.seq) {
		// Cancel-on-entry: a cancel arrived while this accepted run was
		// dispatched but not yet active, and this handle's accept sequence
		// is at or below the session's cancel mark. The mark is left in
		// place so sibling handles it also covers observe the same cancel;
		// release the accept reservation, drop the lock, and persist a
		// canceled turn without entering Stream.
		//
		// This path returns before the streaming defer that publishes
		// RunComplete is installed, so emit the terminal event explicitly.
		// Without it, a caller waiting on RunComplete for this RunID (e.g.
		// `harness run`, which ignores message events and blocks on
		// RunComplete) would hang on an immediately-canceled accepted run.
		call.Accepted.Close()
		sessMu.Unlock()
		complete := notify.RunComplete{
			SessionID: call.SessionID,
			RunID:     call.RunID,
			Cancelled: true,
		}
		if err := a.persistCanceledTurn(ctx, call, false); err != nil {
			complete.Error = err.Error()
			a.publishRunComplete(ctx, call, complete)
			return nil, err
		}
		a.publishRunComplete(ctx, call, complete)
		return nil, nil
	}

	if a.IsSessionBusy(call.SessionID) {
		// Busy: an earlier prompt is active. Queue this call so it is
		// folded into (or sequenced after) the active turn, and release any
		// accept reservation. A Cancel arriving after this point sees the
		// active entry and clears the queue.
		//
		// enqueueCall strips OnComplete: the caller that supplied the hook
		// (typically coordinator.Run) has its own retry/coalesce scope that
		// ends when it returns, so by the time the queue drains nobody is
		// left to consume the buffered terminal event. The queued turn falls
		// back to the default broker publish, which is what existing
		// subscribers expect.
		a.enqueueCall(call)
		if call.Accepted != nil {
			call.Accepted.Close()
		}
		sessMu.Unlock()
		return nil, nil
	}

	// Idle: become the active run. Register the cancel func before dropping
	// the lock so a Cancel that arrives between here and assistant creation
	// is not lost.
	runCtx := context.WithValue(ctx, tools.SessionIDContextKey, call.SessionID)
	genCtx, cancel = context.WithCancel(runCtx)
	ac := &activeCancel{cancel: cancel}
	a.activeRequests.Set(call.SessionID, ac)
	if call.Accepted != nil {
		call.Accepted.Close()
	}
	sessMu.Unlock()

	defer cancel()
	// Conditional cleanup: only remove our entry if it hasn't been replaced
	// by a newer run. Without this guard, the deferred Del fires after a
	// concurrent run registers in the completion window, silently wiping
	// the new run's cancel and breaking cancellation.
	defer csync.CompareAndDelete(a.activeRequests, call.SessionID, ac)

	// Copy mutable fields under lock to avoid races with SetTools/SetModels.
	agentTools := a.tools.Copy()
	largeModel := a.largeModel.Get()
	systemPrompt, runtimePrompt := splitRuntimePrompt(a.systemPrompt.Get())
	activation := NewSkillActivation(a.skillActivation)
	promptPrefix := a.systemPromptPrefix.Get()
	var instructions strings.Builder

	live := liveMCPServers(agentTools)
	states := mcp.GetStates()
	for _, name := range slices.Sorted(maps.Keys(states)) {
		server := states[name]
		if server.State != mcp.StateConnected {
			continue
		}
		// A server whose tools are deferred behind a tool_search stub has
		// nothing callable in this request, so its instructions describe
		// tools the model cannot reach -- the largest of them here ran to
		// 4.5KB of "use these tools, do NOT shell out" for a server with
		// zero tools in the tool list. They are withheld until the tools
		// are loaded, which is when mcpSearchTool hands them over. Servers
		// exposing resources or prompts still count as reachable: those
		// are served by the shared mcp_resource tool, not by a tool of
		// their own.
		if !live[name] && server.Counts.Resources == 0 && server.Counts.Prompts == 0 {
			continue
		}
		if s := server.Client.InitializeResult().Instructions; s != "" {
			instructions.WriteString(s)
			instructions.WriteString("\n\n")
		}
	}

	if s := instructions.String(); s != "" {
		systemPrompt += "\n\n<mcp-instructions>\n" + s + "\n</mcp-instructions>"
	}

	stampToolCacheControl(agentTools, a.getCacheControlOptions())

	agent := fantasy.NewAgent(
		largeModel.Model,
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithTools(agentTools...),
		a.retryOption(),
		fantasy.WithUserAgent(userAgent),
		// Fix what can be fixed from the call itself - malformed JSON, a
		// missing label - rather than spending a turn asking the model
		// to send it again.
		fantasy.WithRepairToolCall(tools.RepairToolCall),
	)

	sessionLock := sync.Mutex{}
	currentSession, err := a.sessions.Get(ctx, call.SessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	msgs, hiddenSummary, err := a.sessionHistory(ctx, currentSession)
	if err != nil {
		return nil, fmt.Errorf("failed to get session messages: %w", err)
	}
	hiddenSummary, execution := splitExecutionSummary(hiddenSummary)
	execution.Ingest(msgs)

	// Generate title from the first real (non-shell) user prompt.
	// can take tens of seconds. Blocking Run on it delays the
	// response to the caller. Use a detached context so the title
	// goroutine survives Run's cancel.
	if !hasUserTextMessage(msgs) {
		titleCtx := context.WithoutCancel(ctx)
		crash.Go("agent.generateTitle", func() { a.GenerateTitle(titleCtx, call.SessionID, call.Prompt) })
	}

	// Fire the pre-prompt hooks: SessionStart on the first turn of a
	// session, then UserPromptSubmit on every dispatched prompt. These
	// run before the user message is persisted so a denial leaves no
	// orphaned turn behind. The outbound prompt is what the model sees;
	// the stored message keeps the prompt exactly as the user typed it.
	outboundPrompt := call.Prompt
	var promptHookContexts []string
	if !a.isSubAgent {
		if !hasUserTextMessage(msgs) && a.hooks.Has(hooks.EventSessionStart) {
			res, hookErr := a.hooks.Run(ctx, hooks.EventContext{
				Event:     hooks.EventSessionStart,
				SessionID: call.SessionID,
				Prompt:    call.Prompt,
			})
			if hookErr != nil {
				slog.Warn("SessionStart hook error", "error", hookErr)
			}
			if res.Context != "" {
				promptHookContexts = append(promptHookContexts, res.Context)
			}
		}
		if a.hooks.Has(hooks.EventUserPromptSubmit) {
			attachments := make([]string, len(call.Attachments))
			for i, att := range call.Attachments {
				attachments[i] = att.FileName
			}
			res, hookErr := a.hooks.Run(ctx, hooks.EventContext{
				Event:       hooks.EventUserPromptSubmit,
				SessionID:   call.SessionID,
				Prompt:      call.Prompt,
				Attachments: attachments,
			})
			if hookErr != nil {
				slog.Warn("UserPromptSubmit hook error", "error", hookErr)
			}
			if res.Decision == hooks.DecisionDeny || res.Halt {
				blockErr := fmt.Errorf("prompt blocked by hook: %s", res.Reason)
				complete := notify.RunComplete{
					SessionID: call.SessionID,
					RunID:     call.RunID,
					Error:     blockErr.Error(),
				}
				a.publishRunComplete(ctx, call, complete)
				return nil, blockErr
			}
			if res.UpdatedPrompt != "" {
				outboundPrompt = res.UpdatedPrompt
			}
			if res.Context != "" {
				promptHookContexts = append(promptHookContexts, res.Context)
			}
		}
	}
	if len(promptHookContexts) > 0 {
		outboundPrompt += "\n\n<hook-context>\n" + strings.Join(promptHookContexts, "\n") + "\n</hook-context>"
	}

	// Add the user message to the session. Its ID is kept so an overflow
	// recovery can take it back out again: the requeued turn writes the
	// prompt afresh after the summary, and leaving this copy behind would
	// show the user their own message twice.
	userMsg, err := a.createUserMessage(ctx, call)
	if err != nil {
		return nil, err
	}
	userMsgCreated = true

	// Publish this instance as mid-turn in the workspace presence
	// registry, so concurrent harness instances see the session being
	// worked on. Sub-agents hold no registry; their edits still publish
	// file activity through the tools.
	if a.presence != nil {
		title := ""
		if sess, sessErr := a.sessions.Get(ctx, call.SessionID); sessErr == nil {
			title = sess.Title
		}
		a.presence.SetSession(call.SessionID, title)
		a.presence.BeginTurn()
		defer a.presence.EndTurn()
	}

	// Add the session to the context. The run context (genCtx) and its
	// cancel func were already created and registered under the dispatch
	// mutex above for both the accepted and in-process paths.
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, call.SessionID)
	// skipRunComplete is set just before the queued-recursion path so
	// the outer Run doesn't publish a RunComplete that would race
	// with — and be superseded by — the recursive call's own
	// RunComplete (each queued user prompt is its own turn and
	// publishes exactly one terminal event).
	var skipRunComplete bool
	// currentAssistant is declared here so the deferred RunComplete
	// publish below can capture the pointer that PrepareStep will
	// later (re)assign for each streaming step. The final assistant
	// message of the turn is the value reachable through this
	// pointer when the defer runs.
	var currentAssistant *message.Message
	// retryAttempt counts OnRetry invocations for this turn so the
	// user-visible retry notice can report progress. Fantasy invokes
	// OnRetry synchronously from its retry loop, so no atomics needed.
	var retryAttempt int
	// projectedRequestTokens holds the estimate of the request the most
	// recent PrepareStep assembled — history plus the tool results that
	// landed since the last reported usage. The reported counters alone
	// cannot see those, so the stop condition checks both.
	var projectedRequestTokens int64
	// requestTokens estimates each step's request. A step's history is
	// the last one's plus a little, and it recounts only what is new.
	requestTokens := newHistoryTokenEstimator()
	// Drain any debounced message updates before returning. message.Service
	// already flushes synchronously on terminal updates, but a defer here
	// guarantees the contract at every Run exit (success, error, panic
	// recovery upstream) without callers needing to know.
	//
	// After the flush completes — meaning all per-message
	// Publish(UpdatedEvent) calls have fired and been buffered into
	// every subscriber's channel — publish the authoritative
	// RunComplete event for this turn. The flush-then-publish order
	// gives well-behaved clients the best chance of seeing the final
	// message event before RunComplete; the embedded Text field
	// reconciles for clients that observe the events out of order
	// (the pubsub broker fan-in does not serialize publishes from
	// different upstream brokers).
	defer func() {
		// Use a context detached from the run context: workspace
		// shutdown cancels ctx before this goroutine returns, but the
		// buffered streaming deltas must still land before the DB is
		// closed. A short timeout bounds the flush.
		flushCtx, flushCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer flushCancel()
		if !skipRunComplete {
			// Report what the servers worked out since the last step of
			// the now-ending turn. A fix on the final step would otherwise
			// never be reported: the step sweep runs before a model step,
			// and there is no next one. The note is appended once at the
			// end of the transcript and replayed at that fixed position on
			// the next turn, so the cached prefix only grows.
			tools.DiagnosticsFinalSweep(flushCtx, a.lspManager, call.SessionID, func(text string) {
				if _, err := a.messages.Create(flushCtx, call.SessionID, message.CreateMessageParams{
					Role:  message.User,
					Parts: []message.ContentPart{message.ContextNote{Kind: message.ContextNoteDiagnostics, Text: text}},
				}); err != nil {
					slog.Error("Failed to persist final diagnostics report", "error", err)
				}
			})
		}
		if flushErr := a.messages.FlushAll(flushCtx); flushErr != nil {
			slog.Error("Failed to flush pending message updates after run", "error", flushErr)
		}
		if skipRunComplete {
			// A queued prompt is about to recurse into its own Run on
			// this session; that run's step sweep and final sweep cover
			// anything in flight, so reporting here would only split one
			// turn's diagnostics across two rows.
			return
		}
		// Prefer the per-call hook when supplied so the coordinator
		// can coalesce retries (e.g. unauthorized → re-auth → retry)
		// into a single user-visible terminal event. The fallback
		// must-deliver publish applies bounded-blocking semantics to
		// the authoritative terminal event so a momentarily-full
		// subscriber channel can't silently drop it and hang
		// non-interactive clients waiting on RunComplete. It gets the
		// detached flushCtx: a canceled run context would make it give
		// up on a full subscriber and drop the event.
		a.publishRunComplete(flushCtx, call, turnComplete(ctx, call, currentAssistant, retErr))
	}()

	history, files := a.preparePrompt(msgs, largeModel.CatalogCfg.SupportsImages, call.Attachments...)
	// Everything compaction folded away rides in as one hidden summary
	// ahead of the verbatim history; see compaction.go.
	history = withSummary(hiddenSummary, history)

	var stepMessages []fantasy.Message
	var shouldSummarize bool
	sanitizedToolCalls := make(map[string]bool)
	// Don't send MaxOutputTokens if 0 — some providers (e.g. LM Studio) reject it
	var maxOutputTokens *int64
	if call.MaxOutputTokens > 0 {
		maxOutputTokens = &call.MaxOutputTokens
	}
	// continuing is set for the step that resumes an answer cut off at
	// max_tokens: PrepareStep then keeps writing into the assistant
	// message the cut happened in instead of opening a new one.
	var continuing bool
	// injections is everything this turn added to its requests beyond
	// what fantasy carries between steps; see turnInjections.
	var injections turnInjections
	streamCall := fantasy.AgentStreamCall{
		Prompt:           message.PromptWithTextAttachments(outboundPrompt, call.Attachments),
		Files:            files,
		Messages:         history,
		Headers:          sessionHeaders(call.SessionID),
		ProviderOptions:  withPromptCacheKey(call.SessionID, call.ProviderOptions),
		MaxOutputTokens:  maxOutputTokens,
		TopP:             call.TopP,
		Temperature:      call.Temperature,
		PresencePenalty:  call.PresencePenalty,
		TopK:             call.TopK,
		FrequencyPenalty: call.FrequencyPenalty,
		PrepareStep: func(callContext context.Context, options fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
			for i := range options.Messages {
				options.Messages[i].ProviderOptions = nil
			}
			// Everything appended below goes in at this index on later
			// steps, behind the messages that exist now and ahead of
			// the ones this step produces.
			appendAt := len(options.Messages)
			prepared.Messages = injections.apply(options.Messages)

			// Use latest tools (updated by SetTools when MCP tools change).
			prepared.Tools = a.tools.Copy()
			// The tool palette can change mid-turn, so keep the breakpoint
			// on the current tail instead of the one Run stamped.
			stampToolCacheControl(prepared.Tools, a.getCacheControlOptions())

			// Drain queued follow-up prompts for this step. Calls covered
			// by a cancel recorded while they sat in the queue are dropped:
			// a cancel that arrived after a prompt was queued must not let
			// it run as part of this step. Coverage is per-call by accept
			// sequence so a follow-up queued after the cancel (higher seq)
			// is not dropped. A dropped prompt carrying a RunID still gets
			// its terminal cancelled RunComplete so a caller waiting on it
			// does not hang. Uncanceled prompts without a RunID are folded
			// into this turn as a single user message joining every drained
			// prompt (they were queued as one steering burst, and the
			// transcript shows them as one entry); uncanceled prompts with
			// a RunID are left queued so each runs as its own turn (with
			// its own RunComplete) via the recursive run path below.
			//
			// A step whose context is already canceled — a CancelTurn that
			// landed between this run registering itself and its first
			// PrepareStep — must not drain the queue: the follow-up prompts
			// belong to the turn that runs after this one unwinds, and
			// folding them here would try to create their user messages on
			// a dead context, silently dropping them.
			var fold, canceledRunIDs []SessionAgentCall
			if callContext.Err() == nil {
				fold, canceledRunIDs = a.drainQueueForStep(call.SessionID)
				a.publishCanceledQueueDrops(canceledRunIDs)
			}
			if len(fold) > 0 {
				userMessage, createErr := a.createUserMessage(callContext, joinQueuedCalls(fold))
				if createErr != nil {
					// The drain removed these calls from the queue; put
					// them back so a failed create does not silently drop
					// the prompts. The turn's error path hands the queue
					// off to the follow-up run.
					a.requeueCalls(call.SessionID, fold)
					return callContext, prepared, createErr
				}
				folded := userMessage.ToAIMessage()
				injections.add(appendAt, folded...)
				prepared.Messages = append(prepared.Messages, folded...)
			}

			// Fold in messages background sub-agents sent this session since
			// the last step. This is the live half of a background dispatch:
			// the message reaches the dispatcher's context — without the
			// child having finished — via a SubagentNote part, which the
			// model reads as user text but the transcript never renders; the
			// background tasks strip is where the report-back surfaces. A
			// canceled step leaves the inbox alone: the messages drain on
			// the next live step instead of failing to persist here.
			if a.subagentInbox != nil && callContext.Err() == nil {
				for _, msg := range a.subagentInbox.DrainSubagentInbox(call.SessionID) {
					userMessage, createErr := a.messages.Create(callContext, call.SessionID, message.CreateMessageParams{
						Role: message.User,
						Parts: []message.ContentPart{message.SubagentNote{
							AgentName:      msg.AgentName,
							Handle:         msg.Handle,
							ChildSessionID: msg.ChildSessionID,
							Text:           msg.Text,
						}},
					})
					if createErr != nil {
						return callContext, prepared, createErr
					}
					note := userMessage.ToAIMessage()
					injections.add(appendAt, note...)
					prepared.Messages = append(prepared.Messages, note...)
				}
			}

			// Context the harness adds for the model - directory
			// instructions, activated skills, language-server reports,
			// the execution state, the runtime environment - is written
			// to the session as a ContextNote row and replayed at this
			// index for the rest of the turn. Sent only in the request
			// that first needed it, each of these sat behind the step's
			// new tool results, past every cached token, and was paid
			// for again on every step; as history it is cached like the
			// rest. A canceled step adds nothing: it is about to fail,
			// and its rows would be created on a dead context.
			inject := func(kind string, produce func([]fantasy.Message) []fantasy.Message) error {
				if callContext.Err() != nil {
					return nil //nolint:nilerr // canceled step adds nothing; the turn fails on its own
				}
				before := len(prepared.Messages)
				next := produce(prepared.Messages)
				for _, msg := range next[before:] {
					text := messageText(msg)
					if text == "" {
						continue
					}
					if _, createErr := a.messages.Create(callContext, call.SessionID, message.CreateMessageParams{
						Role:  message.User,
						Parts: []message.ContentPart{message.ContextNote{Kind: kind, Text: text}},
					}); createErr != nil {
						return createErr
					}
					injections.add(appendAt, msg)
				}
				prepared.Messages = next
				return nil
			}

			// Collect whatever the language servers worked out since the last
			// step. Edits hand their file over and return without waiting, so
			// this is where the analysis of the previous step's writes
			// arrives. Usually there is nothing to say and nothing is added.
			if err = inject(message.ContextNoteDiagnostics, func(msgs []fantasy.Message) []fantasy.Message {
				report := tools.DiagnosticsSweep(callContext, a.lspManager)
				if report == "" {
					return msgs
				}
				return append(msgs, fantasy.NewUserMessage(fmt.Sprintf(
					"<system_reminder>\nLanguage servers reported this since your last step. "+
						"Fix what you caused; ignore the rest. Do not mention this reminder.\n%s</system_reminder>",
					report,
				)))
			}); err != nil {
				return callContext, prepared, err
			}

			// Concurrent harness instances publish themselves and their
			// recent file activity to the workspace presence registry. The
			// note is diff-gated against the last one sent, so a steady
			// state — or no peers at all — sends nothing.
			if err = inject(message.ContextNotePeers, func(msgs []fantasy.Message) []fantasy.Message {
				if a.presence == nil {
					return msgs
				}
				note := peerNote(msgs, a.presence.Peers())
				if note == "" {
					return msgs
				}
				return append(msgs, fantasy.NewUserMessage(note))
			}); err != nil {
				return callContext, prepared, err
			}

			// The execution state is a snapshot, written at the start of
			// a turn when it differs from the last one sent. Not per
			// step: within a turn the tool results themselves say what
			// changed, and a snapshot per step either piles up (every
			// one stays in the history) or has to replace the previous
			// one, which rewrites a sent message and costs the cache.
			if err = inject(message.ContextNoteExecutionState, func(msgs []fantasy.Message) []fantasy.Message {
				if options.StepNumber > 0 {
					return msgs
				}
				snapshot := execution.Render()
				if snapshot == "" || snapshot == lastTaggedUserText(msgs, "<execution_state>") {
					return msgs
				}
				return append(msgs, fantasy.NewUserMessage(snapshot))
			}); err != nil {
				return callContext, prepared, err
			}
			if err = inject(message.ContextNoteDirectoryInstructions, func(msgs []fantasy.Message) []fantasy.Message {
				return a.directoryInstructions.Prepare(callContext, msgs)
			}); err != nil {
				return callContext, prepared, err
			}
			if err = inject(message.ContextNoteSkill, func(msgs []fantasy.Message) []fantasy.Message {
				return activation.Prepare(callContext, msgs)
			}); err != nil {
				return callContext, prepared, err
			}
			// The runtime environment is described once, behind the
			// first prompt; the git status in it is a snapshot from
			// then anyway, and the date rarely turns over mid-session.
			if err = inject(message.ContextNoteRuntime, func(msgs []fantasy.Message) []fantasy.Message {
				if runtimeContextPresent(msgs) {
					return msgs
				}
				return withRuntimeContext(msgs, runtimePrompt)
			}); err != nil {
				return callContext, prepared, err
			}
			prepared.Messages = a.workaroundProviderMediaLimitations(prepared.Messages, largeModel)
			prepared.Messages = mergeConsecutiveUserMessages(prepared.Messages)

			lastSystemRoleInx := 0
			systemMessageUpdated := false
			for i, msg := range prepared.Messages {
				// Only add cache control to the last message.
				if msg.Role == fantasy.MessageRoleSystem {
					lastSystemRoleInx = i
				} else if !systemMessageUpdated {
					prepared.Messages[lastSystemRoleInx].ProviderOptions = a.getCacheControlOptions()
					systemMessageUpdated = true
				}
				// Than add cache control to the last 2 messages.
				if i > len(prepared.Messages)-3 {
					prepared.Messages[i].ProviderOptions = a.getCacheControlOptions()
				}
			}

			if promptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{fantasy.NewSystemMessage(promptPrefix)}, prepared.Messages...)
			}

			sessionLock.Lock()
			stepMessages = cloneFantasyMessages(prepared.Messages)
			sessionLock.Unlock()

			// Project the request this step is about to send, before any row
			// is written for it. The reported session counters cannot see the
			// tool results that landed since the last step, so a request
			// assembled from them can overflow with no check in between.
			// Aborting here, before the assistant message is created and
			// before the provider is called, lets the error path summarize
			// and requeue while leaving nothing behind for a turn that never
			// happened.
			if cw := usableContextWindow(a.largeModel.Get()); cw > 0 && !a.disableAutoSummarize {
				projected := requestTokens.Messages(prepared.Messages)
				sessionLock.Lock()
				projectedRequestTokens = projected
				sessionLock.Unlock()
				threshold := autoSummarizeThreshold(cw, a.autoSummarizeRatio, a.autoSummarizeBuffer)
				if projected+threshold >= cw {
					return callContext, prepared, fmt.Errorf(
						"%w: next request projected at ~%d tokens against a %d-token usable window",
						errContextWindowExceeded, projected, cw,
					)
				}
			}

			if continuing && currentAssistant != nil {
				// Resuming a cut-off answer: keep the assistant message it
				// was cut in, minus the max_tokens finish it was closed
				// with, so the continuation reads as one answer.
				currentAssistant.Parts = slices.DeleteFunc(currentAssistant.Parts, func(p message.ContentPart) bool {
					_, ok := p.(message.Finish)
					return ok
				})
				return stepToolContext(callContext, currentAssistant.ID, largeModel), prepared, nil
			}

			var assistantMsg message.Message
			// The assistant row must exist for the turn's terminal persistence
			// even when a CancelTurn raced this step: create it on a detached
			// context so a dead step context fails the step only after the
			// model observed the cancel, letting the turn unwind through the
			// normal canceled path (and hand off to queued follow-ups).
			assistantCtx, assistantCancel := context.WithTimeout(context.WithoutCancel(callContext), 5*time.Second)
			assistantMsg, err = a.messages.Create(assistantCtx, call.SessionID, message.CreateMessageParams{
				Role:     message.Assistant,
				Parts:    []message.ContentPart{},
				Model:    largeModel.ModelCfg.Model,
				Provider: largeModel.ModelCfg.Provider,
			})
			if err != nil {
				assistantCancel()
				return callContext, prepared, err
			}
			assistantCancel()
			currentAssistant = &assistantMsg
			return stepToolContext(callContext, assistantMsg.ID, largeModel), prepared, nil
		},
		OnReasoningStart: func(id string, reasoning fantasy.ReasoningContent) error {
			currentAssistant.AppendReasoningContent(reasoning.Text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnReasoningDelta: func(id string, text string) error {
			currentAssistant.AppendReasoningContent(text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnReasoningEnd: func(id string, reasoning fantasy.ReasoningContent) error {
			// handle anthropic signature
			if anthropicData, ok := reasoning.ProviderMetadata[anthropic.Name]; ok {
				if reasoning, ok := anthropicData.(*anthropic.ReasoningOptionMetadata); ok {
					currentAssistant.AppendReasoningSignature(reasoning.Signature)
				}
			}
			if googleData, ok := reasoning.ProviderMetadata[google.Name]; ok {
				if reasoning, ok := googleData.(*google.ReasoningMetadata); ok {
					currentAssistant.AppendThoughtSignature(reasoning.Signature, reasoning.ToolID)
				}
			}
			if openaiData, ok := reasoning.ProviderMetadata[openai.Name]; ok {
				if reasoning, ok := openaiData.(*openai.ResponsesReasoningMetadata); ok {
					currentAssistant.SetReasoningResponsesData(reasoning)
				}
			}
			currentAssistant.FinishThinking()
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnTextDelta: func(id string, text string) error {
			// Strip leading newline from initial text content. This is is
			// particularly important in non-interactive mode where leading
			// newlines are very visible.
			if len(currentAssistant.Parts) == 0 {
				text = strings.TrimPrefix(text, "\n")
			}

			currentAssistant.AppendContent(text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnToolInputStart: func(id string, toolName string) error {
			toolCall := message.ToolCall{
				ID:               id,
				Name:             toolName,
				ProviderExecuted: false,
				Finished:         false,
			}
			currentAssistant.AddToolCall(toolCall)
			// Use parent ctx instead of genCtx to ensure the update succeeds
			// even if the request is canceled mid-stream
			return a.messages.Update(ctx, *currentAssistant)
		},
		// The input arrives in pieces; each one lands on the stored call
		// so the transcript can show the command or path as it is typed,
		// the way it shows the answer's text as it streams. The message
		// service debounces these writes like text deltas.
		OnToolInputDelta: func(id, delta string) error {
			currentAssistant.AppendToolCallInput(id, delta)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnRetry: func(err *fantasy.ProviderError, delay time.Duration) {
			slog.Warn("Provider request failed, retrying", providerRetryLogFields(err, delay)...)
			retryAttempt++
			// Surface the retry where the user can see it: without
			// this the turn sits silent through the backoff (up to a
			// minute per attempt) and looks hung, typically on the
			// last tool-call spinner. Best-effort and lossy on
			// purpose: Publish never blocks the retry loop.
			if a.notify != nil {
				reason := "provider request failed"
				if err != nil {
					reason = err.Error()
				}
				a.publishNotification(ctx, notify.Notification{
					SessionID:    call.SessionID,
					SessionTitle: currentSession.Title,
					Type:         notify.TypeAgentRetrying,
					Message: fmt.Sprintf("%s; retrying in %s (attempt %d)",
						reason, delay.Round(time.Millisecond), retryAttempt),
				})
			}
			// Reset streamed content so the retried response doesn't
			// concatenate with partial content from the failed attempt.
			// On the final attempt (no more retries), any partial content
			// stays in the message as useful context beneath the error.
			currentAssistant.ResetStreamedContent()
			if updateErr := a.messages.Update(genCtx, *currentAssistant); updateErr != nil {
				slog.Error("Failed to reset message on retry", "error", updateErr)
			}
		},
		OnAuthRefresh: call.OnAuthRefresh,
		ModelProvider: func() fantasy.LanguageModel {
			m := a.largeModel.Get()
			slog.Info("ModelProvider called",
				"provider", m.ModelCfg.Provider,
				"model", m.ModelCfg.Model)
			return m.Model
		},
		OnToolCall: func(tc fantasy.ToolCallContent) error {
			input, wasSanitized := sanitizeToolInput(tc.ToolName, tc.ToolCallID, tc.Input)
			if wasSanitized {
				sanitizedToolCalls[tc.ToolCallID] = true
			}
			toolCall := message.ToolCall{
				ID:               tc.ToolCallID,
				Name:             tc.ToolName,
				Input:            input,
				ProviderExecuted: false,
				Finished:         true,
			}
			currentAssistant.AddToolCall(toolCall)
			// The execution state locks itself; holding sessionLock over
			// it too only made the other callbacks wait on it.
			execution.Ingest([]message.Message{{Parts: []message.ContentPart{toolCall}}})
			// Use parent ctx instead of genCtx to ensure the update succeeds
			// even if the request is canceled mid-stream
			return a.messages.Update(ctx, *currentAssistant)
		},
		OnToolResult: func(result fantasy.ToolResultContent) error {
			toolResult := a.convertToToolResult(result)
			if sanitizedToolCalls[result.ToolCallID] {
				toolResult.Content = "Tool call failed: arguments were not valid JSON. Please check your tool call format and try again."
				toolResult.IsError = true
			}
			execution.Ingest([]message.Message{{Parts: []message.ContentPart{toolResult}}})
			// Use parent ctx instead of genCtx to ensure the message is created
			// even if the request is canceled mid-stream
			_, createMsgErr := a.messages.Create(ctx, currentAssistant.SessionID, message.CreateMessageParams{
				Role: message.Tool,
				Parts: []message.ContentPart{
					toolResult,
				},
			})
			return createMsgErr
		},
		OnStepFinish: func(stepResult fantasy.StepResult) error {
			for _, w := range stepResult.Warnings {
				slog.Warn("Provider warning", "type", w.Type, "message", w.Message)
			}
			finishReason := message.FinishReasonUnknown
			switch stepResult.FinishReason {
			case fantasy.FinishReasonLength:
				finishReason = message.FinishReasonMaxTokens
			case fantasy.FinishReasonStop:
				finishReason = message.FinishReasonEndTurn
			case fantasy.FinishReasonToolCalls:
				finishReason = message.FinishReasonToolUse
			case fantasy.FinishReasonContentFilter:
				// Provider safety classifier stopped the model
				// (Anthropic stop_reason=refusal, OpenAI content_filter).
				// The TUI owns the display copy; we only persist the
				// reason so the UI can show a REFUSED banner.
				finishReason = message.FinishReasonContentFilter
				slog.Warn(
					"Provider content filter stopped the model",
					"session_id", call.SessionID,
					"finish_reason", string(stepResult.FinishReason),
				)
			}
			// If a tool result halted the turn (e.g. a hook halt or a
			// permission denial), the step ends on FinishReasonToolCalls but
			// the model will not be called again. Treat it as the end of the
			// turn so the UI can render the assistant footer.
			if finishReason == message.FinishReasonToolUse {
				for _, tr := range stepResult.Content.ToolResults() {
					if tr.StopTurn {
						finishReason = message.FinishReasonEndTurn
						break
					}
				}
			}
			currentAssistant.AddFinish(finishReason, "", "")
			stripOrphanThinking(currentAssistant)
			sessionLock.Lock()
			defer sessionLock.Unlock()

			updatedSession, getSessionErr := a.sessions.Get(ctx, call.SessionID)
			if getSessionErr != nil {
				return getSessionErr
			}
			usage, estimated := fallbackStepUsageWith(requestTokens.Messages, stepMessages, stepResult)
			a.updateSessionUsage(largeModel, &updatedSession, usage, a.openrouterCost(stepResult.ProviderMetadata), estimated)
			_, sessionErr := a.sessions.Save(ctx, updatedSession)
			if sessionErr != nil {
				return sessionErr
			}
			currentSession = updatedSession
			return a.messages.Update(genCtx, *currentAssistant)
		},
		StopWhen: []fantasy.StopCondition{
			func(_ []fantasy.StepResult) bool {
				// The usable window is the context window minus the reserved
				// output budget: providers that enforce prompt + max_tokens <=
				// window would reject a request the raw window says fits.
				cw := usableContextWindow(a.largeModel.Get())
				// If the usable window is unknown (0), skip auto-summarize
				// to avoid immediately truncating custom/local models.
				if cw == 0 {
					return false
				}
				tokens := currentSession.CompletionTokens + currentSession.PromptTokens
				sessionLock.Lock()
				if projectedRequestTokens > tokens {
					tokens = projectedRequestTokens
				}
				sessionLock.Unlock()
				remaining := cw - tokens
				threshold := autoSummarizeThreshold(cw, a.autoSummarizeRatio, a.autoSummarizeBuffer)
				if (remaining <= threshold) && !a.disableAutoSummarize {
					shouldSummarize = true
					return true
				}
				return false
			},
			func(steps []fantasy.StepResult) bool {
				return hasRepeatedToolCalls(steps, loopDetectionWindowSize, loopDetectionMaxRepeats)
			},
		},
	}
	result, err = agent.Stream(genCtx, streamCall)

	// An answer cut off at max_tokens is resumed in place: the same
	// history, the partial answer, and an unpersisted nudge to carry on.
	// The transcript sees one assistant message growing, never the nudge.
	for continued := 0; err == nil && continued < maxAnswerContinuations && answerTruncated(result); continued++ {
		slog.Info("Answer hit max_tokens; continuing", "session_id", call.SessionID, "continuation", continued+1)
		continuing = true
		follow := streamCall
		follow.Prompt = continuePrompt
		follow.Files = nil
		follow.Messages = continuationHistory(history, streamCall.Prompt, result)
		var cont *fantasy.AgentResult
		cont, err = agent.Stream(genCtx, follow)
		continuing = false
		if err != nil {
			break
		}
		result = mergeContinuation(result, cont)
	}

	for repairs := 0; err == nil && !shouldSummarize && result != nil && result.Response.FinishReason == fantasy.FinishReasonStop; {
		decision, gateErr := a.completionVerification(genCtx, call.SessionID, repairs)
		if gateErr != nil {
			err = gateErr
			break
		}
		if decision.Allow() {
			break
		}
		if decision.Action == verification.ActionVerify {
			if err = a.runCompletionVerification(genCtx, call.SessionID); err != nil {
				break
			}
			decision, err = a.completionVerification(genCtx, call.SessionID, repairs)
			if err != nil || decision.Allow() {
				break
			}
		}
		if decision.Action != verification.ActionRepair {
			err = fmt.Errorf("completion blocked: %s", decision.Reason)
			break
		}
		repairs++
		follow := streamCall
		follow.Prompt = "Configured verification failed. Inspect the recorded verification results, repair the relevant changes, and verify again."
		follow.Files = nil
		stored, repairSummary, loadErr := a.sessionHistory(genCtx, currentSession)
		if loadErr != nil {
			err = loadErr
			break
		}
		execution.Ingest(stored)
		repairNarrative, _ := splitExecutionSummary(repairSummary)
		follow.Messages, _ = a.preparePrompt(stored, largeModel.CatalogCfg.SupportsImages)
		follow.Messages = withSummary(repairNarrative, follow.Messages)
		var repaired *fantasy.AgentResult
		repaired, err = agent.Stream(genCtx, follow)
		if err == nil {
			result = mergeContinuation(result, repaired)
		}
	}

	// recoverOverflow records that this turn died on a context-window
	// overflow — ours (PrepareStep projection) or the provider's
	// (context-length rejection) — the one failure the session can
	// self-heal: summarize and requeue the prompt once instead of leaving
	// it stuck in the state that broke it.
	var recoverOverflow bool
	if err != nil {
		isCancelErr := errors.Is(err, context.Canceled)
		recoverOverflow = !isCancelErr && !call.OverflowRecovered &&
			(errors.Is(err, errContextWindowExceeded) || isContextLengthError(err))
		slog.Info("Agent stream returned error",
			"error", err.Error(),
			"error_type", fmt.Sprintf("%T", err),
			"is_cancel", isCancelErr)
		if currentAssistant == nil {
			// Cancel-before-assistant-creation window: the run was
			// canceled after activeRequests.Set but before PrepareStep
			// created the assistant message. Without this, the turn
			// would return with no FinishReasonCanceled marker and no
			// user-visible record. The user message was already created
			// above, so persistCanceledTurn only writes the assistant
			// record.
			if isCancelErr {
				if persistErr := a.persistCanceledTurn(ctx, call, userMsgCreated); persistErr != nil {
					return nil, persistErr
				}
			}
			if !isCancelErr && !recoverOverflow {
				return result, err
			}
			// Canceled with no assistant message: fall through to the
			// queue handoff below so queued prompts still run. The
			// failed-turn persistence block is skipped — there is no
			// assistant state to persist.
		} else if persistErr := a.persistFailedTurn(ctx, call.SessionID, currentSession.Title, currentAssistant, largeModel, err, retryAttempt); persistErr != nil {
			return nil, persistErr
		} else if !isCancelErr && !recoverOverflow {
			return nil, err
		}
		// A canceled turn falls through to the queue handoff below
		// instead of returning early: the queue survives a turn-only
		// cancel (CancelTurn), and its prompts run as follow-up turns —
		// the interrupt-and-steer flow. A full Cancel drops the queue
		// itself, so the handoff sees an empty queue and returns.
	}

	// Recover from a context-window overflow: summarize the session and
	// requeue the prompt (marked OverflowRecovered) so it resumes against
	// the compacted history. The queue handoff below runs it as its own
	// turn, mirroring the shouldSummarize continuation path.
	if recoverOverflow {
		slog.Warn("Context window exceeded; summarizing and requeueing the prompt",
			"session_id", call.SessionID, "error", err.Error())
		err = nil
		a.activeRequests.Del(call.SessionID)
		// Clear the wreckage of the turn that did not happen. The failed
		// assistant carries the overflow as a finish error, which the
		// transcript would render as a failure the user has to think
		// about -- but it was recovered from, so there is nothing to
		// report. The user message goes with it because the requeued turn
		// writes the prompt again on the far side of the summary; keeping
		// both would show it twice. Whether the turn had started calling
		// tools decides the requeued prompt's framing, so read it before
		// the assistant goes.
		hadToolCalls := currentAssistant != nil && len(currentAssistant.ToolCalls()) > 0
		if currentAssistant != nil {
			if delErr := a.messages.Delete(ctx, currentAssistant.ID); delErr != nil {
				slog.Warn("Failed to remove the overflowed assistant message", "error", delErr)
			}
			currentAssistant = nil
		}
		if delErr := a.messages.Delete(ctx, userMsg.ID); delErr != nil {
			slog.Warn("Failed to remove the overflowed user message", "error", delErr)
		}
		if _, compactErr := a.maintainContext(genCtx, call.SessionID, call.ProviderOptions, call.OnAuthRefresh, compactAuto, "", true); compactErr != nil {
			return nil, compactErr
		}
		requeued := call
		requeued.OverflowRecovered = true
		if hadToolCalls {
			requeued.Prompt = fmt.Sprintf("The previous session was interrupted because it exceeded the context window and the history was summarized. The initial user request was: `%s`", requeued.Prompt)
		}
		a.appendQueued(call.SessionID, requeued)
	}

	if err == nil && shouldSummarize {
		// The stop condition ended the turn with the window nearly full:
		// fold everything, then let the turn carry on if it was not done.
		if _, compactErr := a.maintainContext(genCtx, call.SessionID, call.ProviderOptions, call.OnAuthRefresh, compactAuto, "", true); compactErr != nil {
			return nil, compactErr
		}
		// If the agent wasn't done...
		if len(currentAssistant.ToolCalls()) > 0 {
			call.Prompt = fmt.Sprintf("The previous session was interrupted because it got too long, the initial user request was: `%s`", call.Prompt)
			a.appendQueued(call.SessionID, call)
		}
	} else if err == nil && !a.disableAutoSummarize {
		// The turn is over and the session is still ours: this is the
		// moment to keep the next request in bounds, so the user never
		// waits on a compaction in the middle of an answer. Usually there
		// is nothing to do; when there is, nothing shows in the
		// transcript. A failure here is not the turn's failure.
		if _, compactErr := a.maintainContext(genCtx, call.SessionID, call.ProviderOptions, call.OnAuthRefresh, compactAuto, "", false); compactErr != nil &&
			!errors.Is(compactErr, context.Canceled) {
			slog.Warn("Context maintenance failed", "session_id", call.SessionID, "error", compactErr)
		}
	}

	// Release active request before publishing the notification.
	// TUI handlers poll IsSessionBusy() and only re-evaluate when a
	// tea.Msg arrives, so the cleanup must precede the notify or
	// subscribers see stale busy state at the moment of receipt.
	a.activeRequests.Del(call.SessionID)
	cancel()

	// Send notification that agent has finished its turn (skip for
	// nested/non-interactive sessions, and for turns that ended in an
	// error or a cancel — those have their own UX).
	if err == nil && !call.NonInteractive && a.notify != nil {
		a.publishNotification(ctx, notify.Notification{
			SessionID:    call.SessionID,
			SessionTitle: currentSession.Title,
			Type:         notify.TypeAgentFinished,
		})
	}

	// Hand off to the next queued prompt (if any) under dispatchMu so
	// the transition from this finished run to the queued run is atomic
	// against a concurrent Cancel. activeRequests for this session was
	// just deleted above, so without the lock there is a window in
	// which the session looks idle and a cancel becomes a no-op that
	// fails to stop the queued prompt. Holding the lock lets us observe
	// a pending cancel recorded against the session and drop the queue
	// instead of running it, and (for the recursion) hand a fresh
	// accept reservation to the dequeued call so acceptedRuns stays > 0
	// across the recursive Run's own dispatch handoff — keeping the
	// session observable to Cancel for the entire transition and
	// closing the dequeue -> re-register window.
	mu := a.sessionMu(call.SessionID)
	mu.Lock()
	next, queuedMessages, drops, ok := a.dequeueNextLocked(call.SessionID)
	mu.Unlock()
	// A dropped prompt carrying a RunID must still publish its terminal
	// cancelled RunComplete so a caller waiting on that RunID does not
	// hang.
	a.publishCanceledQueueDrops(drops)
	if !ok {
		// Stop hooks fire on completed turns only: a turn that ended in
		// an error or was canceled is not a completion. A canceled turn
		// handing off to a queued prompt fires Stop once that follow-up
		// turn completes.
		if err == nil {
			a.fireStopHooks(ctx, call.SessionID)
		}
		return result, err
	}
	// There are queued messages, restart the loop. Suppress the outer
	// defer's emit: it would otherwise observe the recursive Run's retErr
	// (named-return clobbering through the return below) against this
	// turn's MessageID/Text and publish a mixed, racing event.
	skipRunComplete = true
	// Decide whether this turn still owes its own terminal RunComplete.
	// Each submitted prompt with a RunID has its own lifecycle, so a turn
	// that is finished and handing off to a *different* queued prompt must
	// publish its own RunComplete here — leaving it to the recursive turn
	// (which carries a different RunID) would hang a caller waiting on
	// this turn's RunID. The exception is the summarize-continuation path,
	// which re-queues this same call (same RunID) to resume after a
	// summary; in that case the eventual terminal turn for this RunID
	// publishes, so publishing now would double-emit.
	outerOwesRunComplete := call.RunID != "" && !slices.ContainsFunc(queuedMessages, func(q SessionAgentCall) bool {
		return q.RunID == call.RunID
	})
	if outerOwesRunComplete {
		a.publishRunComplete(ctx, call, turnComplete(ctx, call, currentAssistant, nil))
	}
	return a.Run(ctx, next)
}

// stripOrphanThinking drops reasoning content from an assistant
// message that produced nothing else. Thinking is always followed by
// the answer or tool calls of its turn, so a message holding only
// thinking can never end a transcript and is not persisted as one:
// the finish (canceled marker or error banner) stays, the thinking
// goes. Called where a turn's terminal state is persisted.
func stripOrphanThinking(msg *message.Message) {
	if strings.TrimSpace(msg.Content().Text) != "" || len(msg.ToolCalls()) > 0 {
		return
	}
	msg.Parts = slices.DeleteFunc(msg.Parts, func(p message.ContentPart) bool {
		_, ok := p.(message.ReasoningContent)
		return ok
	})
}

// persistFailedTurn writes the terminal state of a streaming turn that
// ended with streamErr: unfinished tool calls are closed with error
// results, the failure (or cancellation) is recorded on the assistant
// message, and a terminal retry notice is published when the turn went
// through retries. It returns nil when the state was persisted and the
// error that stopped the persistence otherwise.
//
// A turn that died before producing anything — no text, no tool
// calls — keeps its finish (the canceled marker or error banner the
// transcript renders) but not its thinking: thinking is always
// followed by the answer or tool calls of its turn, so a transcript
// entry holding only thinking can never be a turn's real ending.
func (a *sessionAgent) persistFailedTurn(
	ctx context.Context,
	sessionID, sessionTitle string,
	currentAssistant *message.Message,
	largeModel Model,
	streamErr error,
	retryAttempt int,
) error {
	isCancelErr := errors.Is(streamErr, context.Canceled)
	// Persist final state with a context detached from the run
	// context. The run context (ctx) is derived from the
	// workspace context, which workspace shutdown cancels before
	// agent goroutines finish; using ctx here would drop the
	// final assistant state. WithoutCancel keeps the values
	// (e.g. session ID) while ignoring cancellation, and a short
	// timeout bounds the cleanup writes.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cleanupCancel()
	// Ensure we finish thinking on error to close the reasoning state.
	currentAssistant.FinishThinking()
	stripOrphanThinking(currentAssistant)
	toolCalls := currentAssistant.ToolCalls()
	// INFO: we use the cleanup context here because the genCtx has been cancelled.
	msgs, listErr := a.messages.List(cleanupCtx, currentAssistant.SessionID)
	if listErr != nil {
		return listErr
	}
	for _, tc := range toolCalls {
		if !tc.Finished {
			tc.Finished = true
			tc.Input = "{}"
			currentAssistant.AddToolCall(tc)
			updateErr := a.messages.Update(cleanupCtx, *currentAssistant)
			if updateErr != nil {
				return updateErr
			}
		}

		found := false
		for _, msg := range msgs {
			if msg.Role == message.Tool {
				for _, tr := range msg.ToolResults() {
					if tr.ToolCallID == tc.ID {
						found = true
						break
					}
				}
			}
			if found {
				break
			}
		}
		if found {
			continue
		}
		content := "There was an error while executing the tool"
		if isCancelErr {
			content = "Error: user cancelled assistant tool calling"
		}
		toolResult := message.ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    content,
			IsError:    true,
			Canceled:   isCancelErr,
		}
		_, createErr := a.messages.Create(cleanupCtx, currentAssistant.SessionID, message.CreateMessageParams{
			Role: message.Tool,
			Parts: []message.ContentPart{
				toolResult,
			},
		})
		if createErr != nil {
			return createErr
		}
	}
	const defaultTitle = "Provider Error"
	linkStyle := lipgloss.NewStyle().Foreground(charmtone.Guac).Underline(true)
	if isCancelErr {
		currentAssistant.AddFinish(message.FinishReasonCanceled, "User canceled request", "")
	} else if requestTimedOutErr, ok := errors.AsType[*requestTimeoutError](streamErr); ok {
		// Checked before the provider branches so a deadline our own
		// request timeout imposed is never reported as a provider error.
		currentAssistant.AddFinish(message.FinishReasonError, "Request timed out", requestTimedOutErr.userMessage())
	} else if providerErr, ok := errors.AsType[*fantasy.ProviderError](streamErr); ok {
		if providerErr.Message == "The requested model is not supported." {
			url := "https://github.com/settings/copilot/features"
			link := linkStyle.Hyperlink(url, "id=copilot").Render(url)
			currentAssistant.AddFinish(
				message.FinishReasonError,
				"Copilot model not enabled",
				fmt.Sprintf("%q is not enabled in Copilot. Go to the following page to enable it. Then, wait 5 minutes before trying again. %s", largeModel.CatalogCfg.Name, link),
			)
		} else {
			currentAssistant.AddFinish(message.FinishReasonError, cmp.Or(stringext.Capitalize(providerErr.Title), defaultTitle), providerErr.Message)
		}
	} else if fantasyErr, ok := errors.AsType[*fantasy.Error](streamErr); ok {
		currentAssistant.AddFinish(message.FinishReasonError, cmp.Or(stringext.Capitalize(fantasyErr.Title), defaultTitle), fantasyErr.Message)
	} else if fantasy.IsTransportError(streamErr) {
		wrapped := fantasy.NewTransportError(streamErr)
		currentAssistant.AddFinish(message.FinishReasonError, stringext.Capitalize(wrapped.Title), wrapped.Message)
	} else {
		currentAssistant.AddFinish(message.FinishReasonError, defaultTitle, streamErr.Error())
	}
	// Note: we use the cleanup context here because the genCtx has been
	// cancelled.
	updateErr := a.messages.Update(cleanupCtx, *currentAssistant)
	if updateErr != nil {
		return updateErr
	}
	// The error is already persisted in the chat above. If this
	// turn went through retries, emit one terminal toast signal:
	// per-attempt notices were status-bar only, so without this
	// an away user would never learn the run actually failed.
	// Cancellations stay silent; the cancel path has its own UX.
	if retryAttempt > 0 && !isCancelErr && a.notify != nil {
		attempts := "1 retry"
		if retryAttempt > 1 {
			attempts = fmt.Sprintf("%d retries", retryAttempt)
		}
		a.publishNotification(ctx, notify.Notification{
			SessionID:    sessionID,
			SessionTitle: sessionTitle,
			Type:         notify.TypeAgentError,
			Message:      fmt.Sprintf("failed after %s: %v", attempts, streamErr),
		})
	}
	return nil
}

// Summarize compacts the session on request: everything sent verbatim so
// far is folded into the hidden summary (see compaction.go).
// instructions optionally steer the summary's focus. Prompts that arrive
// while it runs are queued and run afterwards.
func (a *sessionAgent) Summarize(ctx context.Context, sessionID string, opts fantasy.ProviderOptions, onAuthRefresh func(context.Context, *fantasy.ProviderError) error, instructions string) error {
	// The busy check and the registration happen under the dispatch lock,
	// as in Run, so a concurrent Run cannot also pass the check and start
	// on the same session.
	sessMu := a.sessionMu(sessionID)
	sessMu.Lock()
	if a.IsSessionBusy(sessionID) {
		sessMu.Unlock()
		return ErrSessionBusy
	}
	genCtx, cancel := context.WithCancel(ctx)
	ac := &activeCancel{cancel: cancel}
	a.activeRequests.Set(sessionID, ac)
	sessMu.Unlock()
	defer csync.CompareAndDelete(a.activeRequests, sessionID, ac)
	defer cancel()

	if _, err := a.maintainContext(genCtx, sessionID, opts, onAuthRefresh, compactManual, instructions, true); err != nil {
		return err
	}

	// Release the active request before processing queued messages so that
	// Run() does not see the session as busy.
	a.activeRequests.Del(sessionID)
	cancel()

	// Process any messages that were queued while summarizing, through
	// the same cancel filter and burst join as Run's handoff.
	sessMu.Lock()
	next, _, drops, ok := a.dequeueNextLocked(sessionID)
	sessMu.Unlock()
	a.publishCanceledQueueDrops(drops)
	if !ok {
		return nil
	}
	_, qErr := a.Run(ctx, next)
	return qErr
}

// stampToolCacheControl moves the Anthropic-style cache breakpoint to the
// last tool and clears it from the rest, so a request never carries more
// than one tool breakpoint. The tool palette can change mid-session
// (deferred tools loading, MCP servers joining): Run stamps the tail once,
// but a refreshed palette leaves that stamp behind on an instance that is
// no longer last, or on none at all once the palette is rebuilt.
func stampToolCacheControl(tools []fantasy.AgentTool, opts fantasy.ProviderOptions) {
	for _, tool := range tools {
		tool.SetProviderOptions(fantasy.ProviderOptions{})
	}
	if len(tools) == 0 {
		return
	}
	tools[len(tools)-1].SetProviderOptions(opts)
}

func (a *sessionAgent) getCacheControlOptions() fantasy.ProviderOptions {
	if t, _ := strconv.ParseBool(os.Getenv("HARNESS_DISABLE_ANTHROPIC_CACHE")); t {
		return fantasy.ProviderOptions{}
	}
	return fantasy.ProviderOptions{
		anthropic.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
		bedrock.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
		vercel.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
	}
}

// sessionHeaders returns the HTTP headers we use for cache affinity on
// every LLM request for a given session.
//
// We use the session hash is used instead of the raw UUID so the header
// value is deterministic and opaque.
func sessionHeaders(sessionID string) map[string]string {
	hash := session.HashID(sessionID)
	return map[string]string{
		"x-session-id":       hash,
		"x-session-affinity": hash,
	}
}

// withPromptCacheKey stamps OpenAI's prompt_cache_key onto the request
// options, keyed on the same opaque session hash as sessionHeaders.
// OpenAI's prompt caching is automatic but routing-dependent; the key
// keeps a conversation's requests on the same cache. Only sets the
// field when the options are OpenAI ones (chat completions or
// Responses) and the user has not configured a key of their own.
// Disable with HARNESS_DISABLE_PROMPT_CACHE_KEY.
func withPromptCacheKey(sessionID string, opts fantasy.ProviderOptions) fantasy.ProviderOptions {
	if t, _ := strconv.ParseBool(os.Getenv("HARNESS_DISABLE_PROMPT_CACHE_KEY")); t {
		return opts
	}
	switch o := opts[openai.Name].(type) {
	case *openai.ProviderOptions:
		if o.PromptCacheKey == nil {
			key := session.HashID(sessionID)
			o.PromptCacheKey = &key
		}
	case *openai.ResponsesProviderOptions:
		if o.PromptCacheKey == nil {
			key := session.HashID(sessionID)
			o.PromptCacheKey = &key
		}
	}
	return opts
}

func (a *sessionAgent) createUserMessage(ctx context.Context, call SessionAgentCall) (message.Message, error) {
	parts := []message.ContentPart{message.TextContent{Text: call.Prompt}}
	var attachmentParts []message.ContentPart
	for _, attachment := range call.Attachments {
		attachmentParts = append(attachmentParts, message.BinaryContent{Path: attachment.FilePath, MIMEType: attachment.MimeType, Data: attachment.Content})
	}
	parts = append(parts, attachmentParts...)
	msg, err := a.messages.Create(ctx, call.SessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: parts,
	})
	if err != nil {
		return message.Message{}, fmt.Errorf("failed to create user message: %w", err)
	}

	// Snapshot the working tree as the turn starts, before any tool
	// runs, so a rewind to this message restores the state the prompt
	// was submitted into. A failed snapshot never blocks the turn; it
	// only leaves this turn without a files rewind point.
	if a.checkpoints.Enabled() && !a.isSubAgent {
		if err := a.checkpoints.Snapshot(ctx, call.SessionID, msg.ID); err != nil {
			slog.Warn("Failed to snapshot working tree for rewind", "error", err)
		}
	}
	return msg, nil
}

func (a *sessionAgent) preparePrompt(msgs []message.Message, supportsImages bool, attachments ...message.Attachment) ([]fantasy.Message, []fantasy.FilePart) {
	var history []fantasy.Message
	// Collect all tool call IDs present in assistant messages, then index
	// every tool result by its call ID. Tool results are re-emitted right
	// after the assistant message that requested them instead of at their
	// stored position: messages can be written to a session concurrently
	// (e.g. resuming while a tool is still running), which interleaves
	// user messages between a tool call and its result. LLM APIs require
	// every tool call to be followed by its results before any other
	// message, and strict-adjacency providers (e.g. Kimi, DeepSeek) reject
	// the request otherwise, permanently locking the session.
	knownToolCallIDs := make(map[string]struct{})
	for _, m := range msgs {
		if m.Role != message.Assistant {
			continue
		}
		for _, tc := range m.ToolCalls() {
			knownToolCallIDs[tc.ID] = struct{}{}
		}
	}
	toolResultsByCall := make(map[string][]fantasy.MessagePart)
	for _, m := range msgs {
		if m.Role != message.Tool {
			continue
		}
		for _, aiMsg := range m.ToAIMessage() {
			for _, part := range aiMsg.Content {
				tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
				if !ok {
					// Tool-role ToAIMessage only emits ToolResultParts today;
					// log so unexpected parts do not vanish silently.
					slog.Warn(
						"Dropping unexpected non-tool-result part from tool message",
						"part_type", fmt.Sprintf("%T", part),
					)
					continue
				}
				if _, known := knownToolCallIDs[tr.ToolCallID]; !known {
					slog.Warn(
						"Dropping orphaned tool result with no matching tool call",
						"tool_call_id", tr.ToolCallID,
					)
					continue
				}
				toolResultsByCall[tr.ToolCallID] = append(toolResultsByCall[tr.ToolCallID], part)
			}
		}
	}

	// Reasoning is re-sent only for the current turn. A model's thinking
	// from an earlier turn is the largest thing in a reasoning model's
	// history and the least useful: the answer it led to is right there.
	// Anthropic drops it server-side anyway; OpenAI-compatible gateways
	// (vLLM and kin) take it as reasoning_content and bill every token of
	// it on every request, which is how a modest session on a reasoning
	// model runs out of window in an afternoon.
	currentTurn := lastUserTurnStart(msgs)
	for i, m := range msgs {
		if len(m.Parts) == 0 {
			continue
		}
		// Assistant message without content or tool calls (cancelled before it returned anything).
		// TrimSpace: whitespace-only Text is later stripped by ToAIMessage and llama.cpp 400s the session.
		if m.Role == message.Assistant && len(m.ToolCalls()) == 0 && strings.TrimSpace(m.Content().Text) == "" && m.ReasoningContent().String() == "" {
			continue
		}
		// Tool results are emitted right after their assistant message.
		if m.Role == message.Tool {
			continue
		}
		aiMsgs := m.ToAIMessage()
		if m.Role == message.Assistant && i < currentTurn {
			for j := range aiMsgs {
				aiMsgs[j].Content = withoutReasoning(aiMsgs[j].Content)
			}
			// An assistant message that was nothing but reasoning has
			// nothing left to say.
			aiMsgs = slices.DeleteFunc(aiMsgs, func(msg fantasy.Message) bool { return len(msg.Content) == 0 })
		}
		if !supportsImages {
			for i := range aiMsgs {
				if aiMsgs[i].Role == fantasy.MessageRoleUser {
					aiMsgs[i].Content = filterFileParts(aiMsgs[i].Content)
				}
			}
		}
		history = append(history, aiMsgs...)

		if m.Role == message.Assistant && len(m.ToolCalls()) > 0 {
			history = append(history, toolResultsForCalls(m, toolResultsByCall))
		}
	}

	var files []fantasy.FilePart
	for _, attachment := range attachments {
		if attachment.IsText() {
			continue
		}
		if !supportsImages {
			continue
		}
		files = append(files, fantasy.FilePart{
			Filename:  attachment.FileName,
			Data:      attachment.Content,
			MediaType: attachment.MimeType,
		})
	}

	history = mergeConsecutiveUserMessages(history)

	return history, files
}

// withoutReasoning drops the reasoning parts from an assistant message's
// content, for turns whose thinking is over.
func withoutReasoning(parts []fantasy.MessagePart) []fantasy.MessagePart {
	kept := make([]fantasy.MessagePart, 0, len(parts))
	for _, part := range parts {
		if _, ok := fantasy.AsMessagePart[fantasy.ReasoningPart](part); ok {
			continue
		}
		kept = append(kept, part)
	}
	return kept
}

// filterFileParts removes fantasy.FilePart entries from a slice of message
// parts. Used to strip image attachments from historical user messages when
// the current model does not support them.
func filterFileParts(parts []fantasy.MessagePart) []fantasy.MessagePart {
	filtered := make([]fantasy.MessagePart, 0, len(parts))
	for _, part := range parts {
		if _, ok := fantasy.AsMessagePart[fantasy.FilePart](part); ok {
			continue
		}
		filtered = append(filtered, part)
	}
	return filtered
}

// toolResultsForCalls builds the tool message that must immediately follow
// an assistant message with tool calls. LLM APIs require every tool call to
// be followed by its results before any other message; strict-adjacency
// providers reject the request otherwise. Results are taken from
// toolResultsByCall and consumed, so a result stored in a message that also
// holds results for calls of other assistant messages is emitted exactly
// once, next to the assistant that requested it. Tool calls without any
// stored result (e.g. an interrupted session) receive a synthetic error
// response so the conversation keeps working.
func toolResultsForCalls(m message.Message, toolResultsByCall map[string][]fantasy.MessagePart) fantasy.Message {
	content := make([]fantasy.MessagePart, 0, len(m.ToolCalls()))
	for _, tc := range m.ToolCalls() {
		parts := toolResultsByCall[tc.ID]
		delete(toolResultsByCall, tc.ID)
		if len(parts) > 0 {
			content = append(content, parts...)
			continue
		}
		slog.Warn(
			"Injecting synthetic tool result for orphaned tool call",
			"tool_call_id", tc.ID,
			"tool_name", tc.Name,
		)
		content = append(content, fantasy.ToolResultPart{
			ToolCallID: tc.ID,
			Output: fantasy.ToolResultOutputContentError{
				Error: errors.New("tool call was interrupted and did not produce a result, you may retry this call if the result is still needed"),
			},
		})
	}
	return fantasy.Message{
		Role:    fantasy.MessageRoleTool,
		Content: content,
	}
}

// mergeConsecutiveUserMessages coalesces adjacent user messages by
// concatenating their content. This prevents strict OpenAI-compatible
// providers (e.g., LM Studio) from rejecting the request with
// "consecutive role 'user'" errors after an ESC cancel leaves an empty
// assistant message that is filtered out. File parts and text parts are
// preserved in order.
// isSystemReminder reports whether a message is one the harness synthesized
// rather than something the user wrote.
func isSystemReminder(m fantasy.Message) bool {
	if len(m.Content) == 0 {
		return false
	}
	tp, ok := fantasy.AsMessagePart[fantasy.TextPart](m.Content[0])
	return ok && strings.Contains(tp.Text, "<system_reminder>")
}

func mergeConsecutiveUserMessages(msgs []fantasy.Message) []fantasy.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]fantasy.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == fantasy.MessageRoleUser && len(out) > 0 && out[len(out)-1].Role == fantasy.MessageRoleUser {
			// Do not merge a synthetic system_reminder with a real user
			// message, in either direction: the todo reminder that leads the
			// prompt, and the diagnostics sweep that can land behind the
			// user's own message, both have to stay their own message for
			// cache control and existing test expectations.
			prev := &out[len(out)-1]
			if isSystemReminder(*prev) || isSystemReminder(m) {
				out = append(out, m)
				continue
			}
			prev.Content = append(prev.Content, m.Content...)
			if m.ProviderOptions != nil {
				prev.ProviderOptions = m.ProviderOptions
			}
			continue
		}
		out = append(out, m)
	}
	return out
}

// hasUserTextMessage reports whether any user message in msgs contains
// text content (as opposed to only shell commands or other non-text parts).
func hasUserTextMessage(msgs []message.Message) bool {
	for _, msg := range msgs {
		if msg.Role != message.User {
			continue
		}
		for _, part := range msg.Parts {
			if tc, ok := part.(message.TextContent); ok && tc.Text != "" {
				return true
			}
		}
	}
	return false
}

// GenerateTitle generates a session title based on the initial prompt.
func (a *sessionAgent) GenerateTitle(ctx context.Context, sessionID string, userPrompt string) {
	if userPrompt == "" {
		return
	}

	// Ensure the session always gets a title even if every path below
	// fails or the context is cancelled before we finish.
	var titleSaved bool
	defer func() {
		if !titleSaved {
			fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := a.sessions.Rename(fallbackCtx, sessionID, DefaultSessionName); err != nil {
				slog.Error("Failed to save fallback session title", "error", err)
			}
		}
	}()

	smallModel := a.smallModel.Get()
	largeModel := a.largeModel.Get()
	systemPromptPrefix := a.systemPromptPrefix.Get()

	newAgent := func(m fantasy.LanguageModel, p []byte, tok int64) fantasy.Agent {
		return fantasy.NewAgent(
			m,
			fantasy.WithSystemPrompt(string(p)+"\n /no_think"),
			fantasy.WithMaxOutputTokens(tok),
			// Title generation is best-effort and must not extend detached work.
			fantasy.WithMaxRetries(0),
			fantasy.WithUserAgent(userAgent),
		)
	}

	streamCall := fantasy.AgentStreamCall{
		Prompt:  fmt.Sprintf("Generate a concise title for the following content:\n\n%s\n <think>\n\n</think>", userPrompt),
		Headers: sessionHeaders(sessionID),
		PrepareStep: func(callCtx context.Context, opts fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
			prepared.Messages = opts.Messages
			if systemPromptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{
					fantasy.NewSystemMessage(systemPromptPrefix),
				}, prepared.Messages...)
			}
			return callCtx, prepared, nil
		},
	}

	type modelAttempt struct {
		name  string
		model Model
	}
	attempts := []modelAttempt{
		{"small", smallModel},
		{"large", largeModel},
	}

	var resp *fantasy.AgentResult
	var err error
	var model Model
	var success bool
	for _, attempt := range attempts {
		tok := int64(40)
		if attempt.model.CatalogCfg.CanReason {
			tok = attempt.model.CatalogCfg.DefaultMaxTokens
		}
		agent := newAgent(attempt.model.Model, titlePrompt, tok)
		call := streamCall
		if a.cfg != nil {
			providerCfg, _ := a.cfg.Config().Providers.Get(attempt.model.ModelCfg.Provider)
			call.ProviderOptions = withPromptCacheKey(sessionID, auxiliaryProviderOptions(attempt.model, providerCfg))
		}
		resp, err = agent.Stream(ctx, call)
		if err == nil && resp.Response.FinishReason != fantasy.FinishReasonLength {
			model = attempt.model
			slog.Debug("Generated title with " + attempt.name + " model")
			success = true
			break
		}
		if err != nil {
			slog.Error("Error generating title with "+attempt.name+" model; trying next", "err", err)
		} else {
			slog.Error("Title generation hit token limit with " + attempt.name + " model; trying next")
		}
	}
	if !success {
		// The deferred fallback will save the default session name.
		return
	}

	// Clean up title.
	var title string
	title = strings.ReplaceAll(resp.Response.Content.Text(), "\n", " ")

	// Remove thinking tags if present.
	title = thinkTagRegex.ReplaceAllString(title, "")
	title = orphanThinkTagRegex.ReplaceAllString(title, "")

	title = strings.TrimSpace(title)
	if title == "" {
		// LLM returned empty content. Use the prompt itself as a
		// fallback title, truncated to 50 chars, before resorting to
		// the generic default.
		fallback := strings.ReplaceAll(userPrompt, "\n", " ")
		fallback = strings.TrimSpace(fallback)
		if len(fallback) > 50 {
			fallback = ansi.Truncate(fallback, 50, "…")
		}
		title = cmp.Or(fallback, DefaultSessionName)
	}

	// Calculate usage and cost.
	var openrouterCost *float64
	for _, step := range resp.Steps {
		stepCost := a.openrouterCost(step.ProviderMetadata)
		if stepCost != nil {
			newCost := *stepCost
			if openrouterCost != nil {
				newCost += *openrouterCost
			}
			openrouterCost = &newCost
		}
	}

	modelConfig := model.CatalogCfg
	cost := modelConfig.CostPer1MInCached/1e6*float64(resp.TotalUsage.CacheCreationTokens) +
		modelConfig.CostPer1MOutCached/1e6*float64(resp.TotalUsage.CacheReadTokens) +
		modelConfig.CostPer1MIn/1e6*float64(resp.TotalUsage.InputTokens) +
		modelConfig.CostPer1MOut/1e6*float64(resp.TotalUsage.OutputTokens)

	// Use override cost if available (e.g., from OpenRouter).
	if openrouterCost != nil {
		cost = *openrouterCost
	}

	// Skip cost accumulation
	if model.FlatRate {
		cost = 0
	}

	promptTokens := resp.TotalUsage.InputTokens + resp.TotalUsage.CacheCreationTokens + resp.TotalUsage.CacheReadTokens
	completionTokens := resp.TotalUsage.OutputTokens

	// Atomically update only title and usage fields to avoid overriding other
	// concurrent session updates.
	saveErr := a.sessions.UpdateTitleAndUsage(ctx, sessionID, title, promptTokens, completionTokens, cost)
	if saveErr != nil {
		slog.Error("Failed to save session title and usage", "error", saveErr)
		return
	}
	titleSaved = true
}

func (a *sessionAgent) openrouterCost(metadata fantasy.ProviderMetadata) *float64 {
	openrouterMetadata, ok := metadata[openrouter.Name]
	if !ok {
		return nil
	}

	opts, ok := openrouterMetadata.(*openrouter.ProviderMetadata)
	if !ok {
		return nil
	}
	return &opts.Usage.Cost
}

func (a *sessionAgent) updateSessionUsage(model Model, session *session.Session, usage fantasy.Usage, overrideCost *float64, estimated bool) {
	if !usageIsZero(usage) {
		session.EstimatedUsage = estimated
	}

	modelConfig := model.CatalogCfg
	cost := modelConfig.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		modelConfig.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		modelConfig.CostPer1MIn/1e6*float64(usage.InputTokens) +
		modelConfig.CostPer1MOut/1e6*float64(usage.OutputTokens)

	if estimated {
		cost = 0
	} else {
		// Use override cost if available (e.g., from OpenRouter).
		if overrideCost != nil {
			cost = *overrideCost
		}

		// Skip cost accumulation
		if model.FlatRate {
			cost = 0
		}
	}

	session.Cost += cost
	updateSessionTokenCounters(session, usage)
}

func updateSessionTokenCounters(session *session.Session, usage fantasy.Usage) {
	if usage.OutputTokens != 0 {
		session.CompletionTokens = usage.OutputTokens
	}
	if promptTokens := usage.InputTokens + usage.CacheCreationTokens + usage.CacheReadTokens; promptTokens != 0 {
		session.PromptTokens = promptTokens
	}
}

func (a *sessionAgent) CancelTurn(sessionID string) {
	// Turn-only cancel: interrupt the active request (regular or
	// summarize) and nothing else. Queued prompts and accepted runs are
	// left untouched so they still run once the interrupted turn
	// unwinds — this is the interrupt-and-steer path. Cancel is the
	// drop-everything variant.
	if ac, ok := a.activeRequests.Get(sessionID); ok && ac != nil {
		slog.Debug("Turn cancellation initiated", "session_id", sessionID)
		ac.cancel()
	}
}

func (a *sessionAgent) Cancel(sessionID string) {
	// Serialize against the dispatch handoff in Run so the accepted ->
	// (cancel-on-entry | queued | active) transition is atomic against
	// this cancel. Every cancel observes at least one of: an active
	// request, an accepted run (recorded as a pending cancel), or a
	// queue entry it then clears. If none of those hold, an idle Escape
	// is a true no-op and must not poison the next prompt.
	mu := a.sessionMu(sessionID)
	mu.Lock()
	defer mu.Unlock()

	// Cancel regular requests. Don't use Take() here - we need the entry to
	// remain in activeRequests so IsBusy() returns true until the goroutine
	// fully completes (including error handling that may access the DB).
	// The defer in processRequest will clean up the entry.
	if ac, ok := a.activeRequests.Get(sessionID); ok && ac != nil {
		slog.Debug("Request cancellation initiated", "session_id", sessionID)
		ac.cancel()
	}

	// Record a pending cancel only when a dispatched-but-not-yet-active
	// run exists. This catches runs still in the goroutine scheduler or
	// about to enter Run's busy-queue branch, while leaving an idle
	// session untouched. Active and accepted are not mutually exclusive:
	// when a run is active and a follow-up has been accepted, both the
	// cancel above and this pending record fire.
	//
	// Raise the session's cancel mark to the latest accept sequence
	// assigned so far. Every prompt currently accepted-but-not-yet-
	// active has a sequence at or below that value, so one cancel covers
	// all of them; a prompt accepted after this cancel gets a strictly
	// higher sequence and is never poisoned. Using max keeps repeated
	// cancels idempotent while the same prompts are in flight and lets a
	// later cancel extend coverage to prompts accepted since.
	a.acceptedMu.Lock()
	count, ok := a.acceptedRuns.Get(sessionID)
	mark := a.acceptSeqGen
	a.acceptedMu.Unlock()
	if ok && count > 0 {
		slog.Debug("Recording cancel mark for accepted runs", "session_id", sessionID, "count", count, "mark", mark)
		existing, _ := a.cancelMark.Get(sessionID)
		a.cancelMark.Set(sessionID, max(existing, mark))
	}

	if a.QueuedPrompts(sessionID) > 0 {
		slog.Debug("Clearing queued prompts", "session_id", sessionID)
		a.clearQueueAndNotify(sessionID)
	}
}

func (a *sessionAgent) ClearQueue(sessionID string) {
	if a.QueuedPrompts(sessionID) > 0 {
		slog.Debug("Clearing queued prompts", "session_id", sessionID)
		a.clearQueueAndNotify(sessionID)
	}
}

func (a *sessionAgent) CancelAll() {
	if !a.IsBusy() {
		return
	}
	for key := range a.activeRequests.Seq2() {
		a.Cancel(key) // key is sessionID
	}

	timeout := time.After(5 * time.Second)
	for a.IsBusy() {
		select {
		case <-timeout:
			return
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func (a *sessionAgent) IsBusy() bool {
	var busy bool
	for ac := range a.activeRequests.Seq() {
		if ac != nil {
			busy = true
			break
		}
	}
	return busy
}

func (a *sessionAgent) IsSessionBusy(sessionID string) bool {
	_, busy := a.activeRequests.Get(sessionID)
	return busy
}

func (a *sessionAgent) QueuedPrompts(sessionID string) int {
	l, ok := a.messageQueue.Get(sessionID)
	if !ok {
		return 0
	}
	return len(l)
}

func (a *sessionAgent) QueuedPromptsList(sessionID string) []string {
	l, ok := a.messageQueue.Get(sessionID)
	if !ok {
		return nil
	}
	prompts := make([]string, len(l))
	for i, call := range l {
		prompts[i] = call.Prompt
	}
	return prompts
}

func (a *sessionAgent) SetModels(large Model, small Model) {
	a.largeModel.Set(large)
	a.smallModel.Set(small)
}

func (a *sessionAgent) SetTools(tools []fantasy.AgentTool) {
	a.tools.SetSlice(withResultCap(tools))
}

func (a *sessionAgent) SetSystemPrompt(systemPrompt string) {
	a.systemPrompt.Set(systemPrompt)
}

func (a *sessionAgent) Model() Model {
	return a.largeModel.Get()
}

// validationHint describes the tool a rejected call was aimed at, so
// the model has the shape it needs rather than only the complaint. It
// is empty for every error that is not the agent refusing a call on its
// parameters.
func (a *sessionAgent) validationHint(name string, err error) string {
	if !tools.IsValidationError(err) {
		return ""
	}
	for _, t := range a.tools.Copy() {
		if info := t.Info(); info.Name == name {
			return tools.ValidationHint(info, err)
		}
	}
	return ""
}

// convertToToolResult converts a fantasy tool result to a message tool result.
func (a *sessionAgent) convertToToolResult(result fantasy.ToolResultContent) message.ToolResult {
	baseResult := message.ToolResult{
		ToolCallID: result.ToolCallID,
		Name:       result.ToolName,
		Metadata:   result.ClientMetadata,
	}

	switch result.Result.GetType() {
	case fantasy.ToolResultContentTypeText:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](result.Result); ok {
			baseResult.Content = r.Text
		}
	case fantasy.ToolResultContentTypeError:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result.Result); ok {
			baseResult.Content = r.Error.Error()
			// "missing required parameter: x" names what was wrong and
			// nothing about what right looks like, which leaves the next
			// call to guesswork. Say what the tool actually takes.
			if hint := a.validationHint(result.ToolName, r.Error); hint != "" {
				baseResult.Content += "\n\n" + hint
			}
			baseResult.IsError = true
			// An error that halts the turn is a deliberate stop (e.g. the
			// user cancelled a question), not a failure.
			baseResult.Canceled = result.StopTurn
		}
	case fantasy.ToolResultContentTypeMedia:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](result.Result); ok {
			if !stringext.IsValidBase64(r.Data) {
				slog.Warn(
					"Tool returned media with invalid base64 data, discarding image",
					"tool", result.ToolName,
					"tool_call_id", result.ToolCallID,
				)
				baseResult.Content = "Tool returned image data with invalid encoding"
				baseResult.IsError = true
			} else {
				content := r.Text
				if content == "" {
					content = fmt.Sprintf("Loaded %s content", r.MediaType)
				}
				baseResult.Content = content
				baseResult.Data = r.Data
				baseResult.MIMEType = r.MediaType
			}
		}
	}

	return baseResult
}

// workaroundProviderMediaLimitations converts media content in tool results to
// user messages for providers that don't natively support images in tool results.
//
// Problem: OpenAI, Google, OpenRouter, and other OpenAI-compatible providers
// don't support sending images/media in tool result messages - they only accept
// text in tool results. However, they DO support images in user messages.
//
// If we send media in tool results to these providers, the API returns an error.
//
// Solution: For these providers, we:
//  1. Replace the media in the tool result with a text placeholder
//  2. Inject a user message immediately after with the image as a file attachment
//  3. This maintains the tool execution flow while working around API limitations
//
// Anthropic and Bedrock support images natively in tool results, so we skip
// this workaround for them.
//
// Example transformation:
//
//	BEFORE: [tool result: image data]
//	AFTER:  [tool result: "Image loaded - see attached"], [user: image attachment]
func (a *sessionAgent) workaroundProviderMediaLimitations(messages []fantasy.Message, largeModel Model) []fantasy.Message {
	providerSupportsMedia := largeModel.ModelCfg.Provider == string(catalog.InferenceProviderAnthropic) ||
		largeModel.ModelCfg.Provider == string(catalog.InferenceProviderBedrock)

	if providerSupportsMedia {
		return messages
	}

	supportsImages := largeModel.CatalogCfg.SupportsImages

	convertedMessages := make([]fantasy.Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Role != fantasy.MessageRoleTool {
			convertedMessages = append(convertedMessages, msg)
			continue
		}

		textParts := make([]fantasy.MessagePart, 0, len(msg.Content))
		var mediaFiles []fantasy.FilePart

		for _, part := range msg.Content {
			toolResult, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			if !ok {
				textParts = append(textParts, part)
				continue
			}

			if media, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](toolResult.Output); ok {
				if !supportsImages {
					// Model cannot process images. Replace with a text
					// placeholder and skip creating a synthetic user
					// message with FilePart, which would brick the
					// session on text-only models.
					textParts = append(textParts, fantasy.ToolResultPart{
						ToolCallID: toolResult.ToolCallID,
						Output: fantasy.ToolResultOutputContentText{
							Text: "[Image/media content not supported by this model]",
						},
						ProviderOptions: toolResult.ProviderOptions,
					})
					continue
				}

				decoded, err := base64.StdEncoding.DecodeString(media.Data)
				if err != nil {
					slog.Warn("Failed to decode media data", "error", err)
					textParts = append(textParts, part)
					continue
				}

				mediaFiles = append(mediaFiles, fantasy.FilePart{
					Data:      decoded,
					MediaType: media.MediaType,
					Filename:  fmt.Sprintf("tool-result-%s", toolResult.ToolCallID),
				})

				textParts = append(textParts, fantasy.ToolResultPart{
					ToolCallID: toolResult.ToolCallID,
					Output: fantasy.ToolResultOutputContentText{
						Text: "[Image/media content loaded - see attached file]",
					},
					ProviderOptions: toolResult.ProviderOptions,
				})
			} else {
				textParts = append(textParts, part)
			}
		}

		convertedMessages = append(convertedMessages, fantasy.Message{
			Role:    fantasy.MessageRoleTool,
			Content: textParts,
		})

		if len(mediaFiles) > 0 {
			convertedMessages = append(convertedMessages, fantasy.NewUserMessage(
				"Here is the media content from the tool result:",
				mediaFiles...,
			))
		}
	}

	return convertedMessages
}

// buildSummaryPrompt constructs the prompt text for session summarization.
// instructions, when non-empty, steers what the summary focuses on.
func providerRetryLogFields(err *fantasy.ProviderError, delay time.Duration) []any {
	fields := []any{
		"retry_delay", delay.String(),
	}
	if err == nil {
		return fields
	}
	fields = append(fields, "status_code", err.StatusCode)
	if err.Title != "" {
		fields = append(fields, "title", err.Title)
	}
	if err.Message != "" {
		fields = append(fields, "message", err.Message)
	}
	return fields
}

// sanitizeToolInput validates tool call JSON from the provider.
// Malformed input is replaced with an empty object to prevent
// stuck conversations from truncated or malformed model output.
// The second return value indicates whether sanitization occurred.
func sanitizeToolInput(toolName, toolCallID, input string) (string, bool) {
	if !json.Valid([]byte(input)) {
		slog.Warn(
			"Malformed tool call JSON from provider, replacing with empty object",
			"tool", toolName,
			"id", toolCallID,
			"input_len", len(input),
		)
		return "{}", true
	}
	return input, false
}
