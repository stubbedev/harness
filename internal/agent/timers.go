package agent

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/stubbedev/harness/internal/agent/tools"
)

const (
	// maxSessionTimers caps how many timers one session may have armed
	// at once. Past it arming fails with a message the model can read: a
	// fleet of timers spends a wakeup each and every one of them would
	// have to be waited on or forgotten.
	maxSessionTimers = 8

	// timerIDPrefix names a timer's id. The number after it is minted
	// per session, so timer-1 in one session is not another's.
	timerIDPrefix = "timer-"
)

// The bounds on a timer's duration, taken from the tool that documents
// them for the model so the description and the rejection cannot drift.
var (
	minTimerDuration = time.Duration(tools.MinTimerSeconds) * time.Second
	maxTimerDuration = time.Duration(tools.MaxTimerSeconds) * time.Second
)

// TimerState is where a primed timer's life stands. A timer is only
// collectable once it is no longer armed.
type TimerState string

const (
	// TimerArmed is a timer whose duration has not elapsed yet.
	TimerArmed TimerState = "armed"
	// TimerFired is a timer that reached its duration. It stays fired:
	// a timer is one shot and never re-arms itself.
	TimerFired TimerState = "fired"
	// TimerCancelled is a timer stopped by an explicit cancel, its
	// session's cancel or the coordinator's shutdown.
	TimerCancelled TimerState = "cancelled"
)

// TimerInfo is a point-in-time snapshot of a timer: enough to report it,
// wait on it and collect its result without holding the record open.
// Remaining is derived from the timestamps taken under the record's
// lock, so it is consistent with them whatever the caller's clock.
type TimerInfo struct {
	ID        string
	State     TimerState
	Duration  time.Duration
	ArmedAt   time.Time
	FiresAt   time.Time
	Remaining time.Duration
	Collected bool
}

// sessionTimer is one primed timer a session armed: its record outlives
// the call that armed it (and the turn), so a later wait can collect the
// result exactly once.
type sessionTimer struct {
	id            string
	parentSession string
	duration      time.Duration

	mu        sync.Mutex
	state     TimerState
	armedAt   time.Time
	firesAt   time.Time
	collected bool
}

// info returns the timer's current state.
func (t *sessionTimer) info(now time.Time) TimerInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	info := TimerInfo{
		ID:        t.id,
		State:     t.state,
		Duration:  t.duration,
		ArmedAt:   t.armedAt,
		FiresAt:   t.firesAt,
		Collected: t.collected,
	}
	if t.state == TimerArmed {
		info.Remaining = max(t.firesAt.Sub(now), 0)
	}
	return info
}

// fire moves an armed timer to fired, exactly once, and reports whether
// it did: a cancelled timer's channel can still deliver its tick, and
// that tick must not fire it.
func (t *sessionTimer) fire() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != TimerArmed {
		return false
	}
	t.state = TimerFired
	return true
}

// cancel moves an armed timer to cancelled and reports whether it did:
// a timer that already fired has nothing left to cancel.
func (t *sessionTimer) cancel() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != TimerArmed {
		return false
	}
	t.state = TimerCancelled
	return true
}

// collect marks a fired or cancelled timer's result as taken, so exactly
// one wait hands it back; an armed timer has no result to collect yet.
func (t *sessionTimer) collect() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == TimerArmed || t.collected {
		return false
	}
	t.collected = true
	return true
}

// isCollected reports whether a timer's result has already been handed
// out.
func (t *sessionTimer) isCollected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.collected
}

// sessionTimers is one session's timers, in the order they were armed,
// with the count of the ones still armed for the per-session cap.
type sessionTimers struct {
	seq    int
	armed  int
	timers []*sessionTimer
}

// timerRegistry owns every session's primed timers, keyed by the session
// that armed them: a timer id is unique and monotonic within its own
// session, and no session can see another's timers.
//
// A timer fires once, from a goroutine waiting on the clock's channel,
// and its result waits in the record until a later wait collects it.
//
// Wakeups use the close-and-replace channels of signalMap (the same
// pattern as jobRegistry and liveInbox): a waiter fetches the session's
// channel before reading timer state and selects on it after, so a fire
// or cancel landing between the read and the wait still wakes it. The
// channel is broadcast once per fire and once per cancel, which is
// exactly what a wait needs to re-check every timer's state.
type timerRegistry struct {
	mu       sync.Mutex
	sessions map[string]*sessionTimers
	signals  signalMap

	// now and after are the registry's clock. Tests replace both with a
	// fake one, so a timer fires without any test sleeping on the wall
	// clock.
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}

func newTimerRegistry() *timerRegistry {
	return &timerRegistry{
		sessions: make(map[string]*sessionTimers),
		now:      time.Now,
		after:    time.After,
	}
}

// arm starts a one-shot timer of parentSession that fires after d. The
// call returns as soon as the timer is recorded: nothing here blocks on
// the duration.
func (r *timerRegistry) arm(parentSession string, d time.Duration) (*sessionTimer, error) {
	if err := validateTimerDuration(d); err != nil {
		return nil, err
	}
	timer, err := r.reserve(parentSession, d)
	if err != nil {
		return nil, err
	}
	fire := r.after(d)
	go r.run(timer, fire)
	return timer, nil
}

// validateTimerDuration rejects a duration outside the model-facing
// bound, naming that bound.
func validateTimerDuration(d time.Duration) error {
	if d < minTimerDuration || d > maxTimerDuration {
		return fmt.Errorf(
			"a timer must be armed for between %d and %d seconds, got %s",
			tools.MinTimerSeconds, tools.MaxTimerSeconds, d,
		)
	}
	return nil
}

// reserve mints the session's next timer id and adds its record,
// refusing past the per-session cap. The slot is counted here, under the
// lock, so concurrent arms cannot both take the last one.
func (r *timerRegistry) reserve(parentSession string, d time.Duration) (*sessionTimer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session := r.sessions[parentSession]
	if session == nil {
		session = &sessionTimers{}
		r.sessions[parentSession] = session
	}
	if session.armed >= maxSessionTimers {
		return nil, fmt.Errorf(
			"this session already has %d timers armed; wait for one to fire before arming another",
			maxSessionTimers,
		)
	}
	session.seq++
	now := r.now()
	timer := &sessionTimer{
		id:            fmt.Sprintf("%s%d", timerIDPrefix, session.seq),
		parentSession: parentSession,
		duration:      d,
		state:         TimerArmed,
		armedAt:       now,
		firesAt:       now.Add(d),
	}
	session.armed++
	session.timers = append(session.timers, timer)
	return timer, nil
}

// run waits for a timer's tick and records its fire, then wakes everyone
// waiting on the session. It is the only place a timer fires, so a fire
// broadcasts exactly once and never happens twice.
func (r *timerRegistry) run(timer *sessionTimer, fire <-chan time.Time) {
	<-fire
	if !timer.fire() {
		// Cancelled while its tick was in flight.
		return
	}
	r.mu.Lock()
	r.releaseLocked(timer.parentSession)
	r.mu.Unlock()
	r.signals.Broadcast(timer.parentSession)
}

// releaseLocked frees an armed slot. Callers hold mu.
func (r *timerRegistry) releaseLocked(parentSession string) {
	if session := r.sessions[parentSession]; session != nil && session.armed > 0 {
		session.armed--
	}
}

// cancel stops one armed timer, leaves its record in place so the
// cancelled result is still collectable, and wakes the session's
// waiters.
func (r *timerRegistry) cancel(parentSession, timerID string) error {
	timer := r.lookup(parentSession, timerID)
	if timer == nil {
		return fmt.Errorf("unknown timer %q", timerID)
	}
	r.mu.Lock()
	cancelled := timer.cancel()
	if cancelled {
		r.releaseLocked(parentSession)
	}
	r.mu.Unlock()
	if !cancelled {
		return fmt.Errorf("timer %s is not armed", timerID)
	}
	r.signals.Broadcast(parentSession)
	return nil
}

// cancelSession cancels every armed timer of a session and wakes its
// waiters once. The records stay: the session lives on, and a cancelled
// timer's result is still collectable exactly once.
func (r *timerRegistry) cancelSession(parentSession string) {
	r.mu.Lock()
	cancelled := false
	if session := r.sessions[parentSession]; session != nil {
		for _, timer := range session.timers {
			if timer.cancel() {
				cancelled = true
			}
		}
		session.armed = 0
	}
	r.mu.Unlock()
	if cancelled {
		r.signals.Broadcast(parentSession)
	}
}

// cancelAll cancels every timer of every session: the coordinator is
// going away and no timer may fire into it.
func (r *timerRegistry) cancelAll() {
	r.mu.Lock()
	sessions := make([]string, 0, len(r.sessions))
	for session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	for _, session := range sessions {
		r.cancelSession(session)
	}
}

// lookup returns a session's timer record, or nil when that session
// never armed the id: timer ids are per session, so another session's
// timer is indistinguishable from one that does not exist.
func (r *timerRegistry) lookup(parentSession, timerID string) *sessionTimer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session := r.sessions[parentSession]; session != nil {
		for _, timer := range session.timers {
			if timer.id == timerID {
				return timer
			}
		}
	}
	return nil
}

// timers returns a snapshot of every timer the session has armed so far,
// oldest first: what a later wait reports without holding a record open.
func (r *timerRegistry) timers(parentSession string) []TimerInfo {
	now := r.now()
	r.mu.Lock()
	var records []*sessionTimer
	if session := r.sessions[parentSession]; session != nil {
		records = slices.Clone(session.timers)
	}
	r.mu.Unlock()
	infos := make([]TimerInfo, 0, len(records))
	for _, timer := range records {
		infos = append(infos, timer.info(now))
	}
	return infos
}

// collect takes a fired or cancelled timer's result, handing it out at
// most once.
func (r *timerRegistry) collect(parentSession, timerID string) bool {
	timer := r.lookup(parentSession, timerID)
	return timer != nil && timer.collect()
}

// signalChan returns the session's current wakeup channel. Fetch it
// before reading timer state and select on it after.
func (r *timerRegistry) signalChan(parentSession string) chan struct{} {
	return r.signals.Chan(parentSession)
}

// ArmTimer implements tools.TimerArmer: it arms a one-shot timer of
// sessionID's session and returns the id and when it fires. ctx is
// deliberately unused: the timer outlives the call that armed it.
func (c *coordinator) ArmTimer(_ context.Context, sessionID string, seconds int) (tools.Timer, error) {
	timer, err := c.timers.arm(sessionID, time.Duration(seconds)*time.Second)
	if err != nil {
		return tools.Timer{}, err
	}
	info := timer.info(c.timers.now())
	return tools.Timer{ID: info.ID, Duration: info.Duration, FiresAt: info.FiresAt}, nil
}
