// Package pubsub provides a lightweight in-process broker for fan-out
// event delivery between services and the UI.
//
// Delivery semantics:
//
//   - [Broker.Publish] is best-effort and lossy under contention. If a
//     subscriber's channel is full, the event is dropped for that
//     subscriber, a warning is logged, and a counter is incremented.
//     This is the right choice for high-frequency intermediate updates
//     (e.g. streaming token deltas) where only the latest state
//     matters.
//
//   - [Broker.PublishMustDeliver] is bounded-blocking. For each
//     subscriber it first tries a non-blocking send, then falls back to
//     a per-subscriber blocking send with a hard timeout. On timeout the
//     event is dropped for that subscriber, an error is logged, and the
//     must-deliver drop counter is incremented. The publisher never
//     blocks indefinitely. This is the right choice for terminal events
//     (finish, tool result, error, cancel) that must not be silently
//     coalesced away.
//
//   - An evicting broker ([NewEvictingStreamingBroker]) never waits on a
//     subscriber: a must-deliver event that finds a subscriber's buffer
//     full disconnects that subscriber by closing its channel. This is
//     the right choice for subscribers that resync from scratch when
//     their channel closes, such as remote event streams, so one that
//     stalls cannot slow delivery to the rest.
//
// Drop counters ([Broker.DropCount], [Broker.MustDeliverDropCount]) are
// exposed so callers can surface saturation in telemetry.
package pubsub

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// streamingBufferSize is the per-subscriber channel capacity for
	// brokers that carry high-frequency streaming updates (roughly one
	// event per token). Publish is non-blocking, so a full buffer drops
	// events (with a warning log); sized to cover a long streaming
	// assistant turn even under TUI render stalls.
	streamingBufferSize = 4096

	// bufferSize is the default per-subscriber channel capacity. Most
	// brokers carry low-frequency events (session lifecycle, LSP state,
	// notifications, skills, questions) that rarely burst, so a smaller
	// buffer keeps each subscriber channel cheap while still absorbing
	// short spikes without dropping.
	bufferSize = 256

	// defaultMustDeliverTimeout is the per-subscriber upper bound on how
	// long [Broker.PublishMustDeliver] will block waiting for buffer
	// space before giving up on that subscriber.
	defaultMustDeliverTimeout = 50 * time.Millisecond
)

type Broker[T any] struct {
	subs               map[chan Event[T]]struct{}
	mu                 sync.RWMutex
	done               chan struct{}
	channelBufferSize  int
	mustDeliverTimeout time.Duration
	// evictFull disconnects a subscriber whose buffer is full when a
	// must-deliver event reaches it, instead of waiting for room.
	evictFull bool
	dropCount atomic.Uint64
	// dropLoggedAt and dropLoggedCount are when the last drop warning
	// was written and dropCount as of then: a saturated subscriber drops
	// thousands of events a second, and one line per drop buries the log.
	dropLoggedAt         atomic.Int64
	dropLoggedCount      atomic.Uint64
	mustDeliverDropCount atomic.Uint64
}

func NewBroker[T any]() *Broker[T] {
	return NewBrokerWithOptions[T](bufferSize)
}

// NewStreamingBroker creates a broker with the large per-subscriber
// buffer used for high-frequency streaming updates. Prefer
// [NewBroker] for everything else; the smaller default keeps each
// subscriber channel cheap.
func NewStreamingBroker[T any]() *Broker[T] {
	return NewBrokerWithOptions[T](streamingBufferSize)
}

// NewEvictingStreamingBroker creates a streaming broker that disconnects
// a subscriber whose buffer is full when a must-deliver event reaches it,
// rather than blocking on it. Only subscribers that recover from a
// closed channel by resyncing may use it.
func NewEvictingStreamingBroker[T any]() *Broker[T] {
	b := NewBrokerWithOptions[T](streamingBufferSize)
	b.evictFull = true
	return b
}

func NewBrokerWithOptions[T any](channelBufferSize int) *Broker[T] {
	return &Broker[T]{
		subs:               make(map[chan Event[T]]struct{}),
		done:               make(chan struct{}),
		channelBufferSize:  channelBufferSize,
		mustDeliverTimeout: defaultMustDeliverTimeout,
	}
}

// SetMustDeliverTimeout overrides the per-subscriber timeout used by
// [Broker.PublishMustDeliver]. A zero or negative value resets to the
// default. Intended primarily for tests.
func (b *Broker[T]) SetMustDeliverTimeout(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d <= 0 {
		b.mustDeliverTimeout = defaultMustDeliverTimeout
		return
	}
	b.mustDeliverTimeout = d
}

// Shutdown closes every subscriber channel and stops delivery. Safe to
// call more than once and concurrently: the check and close of done
// happen under the lock, so only one caller closes it.
func (b *Broker[T]) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()

	select {
	case <-b.done: // Already closed
		return
	default:
		close(b.done)
	}

	for ch := range b.subs {
		delete(b.subs, ch)
		close(ch)
	}
}

func (b *Broker[T]) Subscribe(ctx context.Context) <-chan Event[T] {
	b.mu.Lock()
	defer b.mu.Unlock()

	select {
	case <-b.done:
		ch := make(chan Event[T])
		close(ch)
		return ch
	default:
	}

	sub := make(chan Event[T], b.channelBufferSize)
	b.subs[sub] = struct{}{}

	go func() {
		<-ctx.Done()

		b.mu.Lock()
		defer b.mu.Unlock()

		select {
		case <-b.done:
			return
		default:
		}

		b.removeLocked(sub)
	}()

	return sub
}

// removeLocked unsubscribes sub and closes its channel, unless it was
// removed already. Caller holds b.mu for writing.
func (b *Broker[T]) removeLocked(sub chan Event[T]) {
	if _, ok := b.subs[sub]; !ok {
		return
	}
	delete(b.subs, sub)
	close(sub)
}

// evict disconnects the subscribers in full, whose buffers had no room
// for a must-deliver event.
func (b *Broker[T]) evict(t EventType, full []chan Event[T]) {
	if len(full) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.done:
		return
	default:
	}
	for _, sub := range full {
		b.removeLocked(sub)
	}
	b.mustDeliverDropCount.Add(uint64(len(full)))
	slog.Warn("Disconnecting pubsub subscribers too slow for a must-deliver event",
		"type", t, "subscribers", len(full))
}

func (b *Broker[T]) GetSubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// DropCount returns the cumulative number of events dropped by
// [Broker.Publish] because a subscriber's channel was full.
func (b *Broker[T]) DropCount() uint64 {
	return b.dropCount.Load()
}

// MustDeliverDropCount returns the cumulative number of events dropped
// by [Broker.PublishMustDeliver] after the per-subscriber timeout
// expired.
func (b *Broker[T]) MustDeliverDropCount() uint64 {
	return b.mustDeliverDropCount.Load()
}

// Publish delivers an event to every active subscriber.
//
// Delivery is non-blocking and lossy: if a subscriber's channel is full
// the event is dropped for that subscriber, a warning is logged, and
// [Broker.DropCount] is incremented. Use [Broker.PublishMustDeliver]
// for events that must not be silently dropped.
func (b *Broker[T]) Publish(t EventType, payload T) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	select {
	case <-b.done:
		return
	default:
	}

	event := Event[T]{Type: t, Payload: payload}

	for sub := range b.subs {
		select {
		case sub <- event:
		default:
			// Channel is full, subscriber is slow — skip this event.
			// Lossy by design; counted and logged so saturation is
			// observable.
			b.logDrop(t, b.dropCount.Add(1))
		}
	}
}

// dropLogInterval is the least time between two drop warnings.
const dropLogInterval = time.Second

// logDrop warns about dropped events at most once per dropLogInterval,
// with the number dropped since the last warning.
func (b *Broker[T]) logDrop(t EventType, total uint64) {
	now := time.Now().UnixNano()
	last := b.dropLoggedAt.Load()
	if last != 0 && now-last < int64(dropLogInterval) {
		return
	}
	if !b.dropLoggedAt.CompareAndSwap(last, now) {
		return
	}
	dropped := total - b.dropLoggedCount.Swap(total)
	slog.Warn("Pubsub buffer full; dropping events", "type", t, "dropped", dropped)
}

// PublishMustDeliver delivers an event with bounded-blocking semantics.
// For each subscriber it first attempts a non-blocking send, then falls
// back to a blocking send bounded by a per-subscriber timeout (default
// [defaultMustDeliverTimeout]). On timeout the event is dropped for
// that subscriber, [Broker.MustDeliverDropCount] is incremented, and an
// error is logged. The publisher never blocks indefinitely.
//
// Use this for terminal events that must reach subscribers (finish,
// tool result, error, cancel). Callers must still tolerate rare drops
// after timeout — recovery is the subscriber's responsibility (e.g. a
// re-fetch on the next session-visible event). A canceled ctx stops the
// blocking wait, not the delivery: the remaining subscribers still get
// the non-blocking attempt. An evicting broker does not wait at all: it
// disconnects every subscriber the non-blocking attempt found full.
func (b *Broker[T]) PublishMustDeliver(ctx context.Context, t EventType, payload T) {
	b.evict(t, b.publishMustDeliver(ctx, t, payload))
}

// publishMustDeliver delivers a must-deliver event and returns the
// subscribers an evicting broker found full, for the caller to evict
// once the read lock is released.
func (b *Broker[T]) publishMustDeliver(ctx context.Context, t EventType, payload T) []chan Event[T] {
	b.mu.RLock()
	defer b.mu.RUnlock()

	select {
	case <-b.done:
		return nil
	default:
	}

	event := Event[T]{Type: t, Payload: payload, MustDeliver: true}
	timeout := b.mustDeliverTimeout

	var full []chan Event[T]
	for sub := range b.subs {
		// Fast path: non-blocking send.
		select {
		case sub <- event:
			continue
		default:
		}
		if b.evictFull {
			full = append(full, sub)
			continue
		}
		if ctx.Err() != nil {
			b.mustDeliverDropCount.Add(1)
			slog.Error("PublishMustDeliver canceled before delivering event", "type", t)
			continue
		}

		// Slow path: bounded blocking send.
		timer := time.NewTimer(timeout)
		select {
		case sub <- event:
			timer.Stop()
		case <-timer.C:
			b.mustDeliverDropCount.Add(1)
			slog.Error("PublishMustDeliver timed out delivering event",
				"type", t, "timeout", timeout)
		case <-ctx.Done():
			timer.Stop()
			b.mustDeliverDropCount.Add(1)
			slog.Error("PublishMustDeliver canceled before delivering event", "type", t)
		}
	}
	return full
}
