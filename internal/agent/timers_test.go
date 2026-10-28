package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock is the timer registry's injectable clock: a timer arms
// against it and Advance fires whatever came due, so no test sleeps on
// the wall clock.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*fakeWait
}

// fakeWait is one pending After, due at at.
type fakeWait struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, &fakeWait{at: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves the clock forward and delivers a tick to every waiter
// that came due, once each.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	due := make([]*fakeWait, 0, len(c.waiters))
	pending := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(now) {
			pending = append(pending, w)
			continue
		}
		due = append(due, w)
	}
	c.waiters = pending
	c.mu.Unlock()
	for _, w := range due {
		w.ch <- now
	}
}

func newTestTimerRegistry(t *testing.T) (*timerRegistry, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	reg := newTimerRegistry()
	reg.now = clock.Now
	reg.after = clock.After
	return reg, clock
}

// armedCount is a session's armed timer count, read under the registry's
// lock the way the registry reads it.
func (r *timerRegistry) armedCount(session string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[session]; s != nil {
		return s.armed
	}
	return 0
}

// waitWoken blocks until the session's wakeup channel is broadcast, so a
// broken fire fails the test instead of hanging it.
func waitWoken(t *testing.T, wake chan struct{}) {
	t.Helper()
	select {
	case <-wake:
	case <-time.After(5 * time.Second):
		require.Fail(t, "the timer's wakeup never arrived")
	}
}

// waitFire waits for one broadcast on wake, then returns the session's
// next channel so the following event can be waited for in turn.
func waitFire(t *testing.T, reg *timerRegistry, session string, wake chan struct{}) chan struct{} {
	t.Helper()
	waitWoken(t, wake)
	return reg.signalChan(session)
}

// Arming returns at once with a per-session id and the times it was
// armed at and will fire at, and its snapshot reports the time left.
func TestTimerArmsWithoutBlocking(t *testing.T) {
	t.Parallel()

	reg, clock := newTestTimerRegistry(t)

	type armed struct {
		timer *sessionTimer
		err   error
	}
	done := make(chan armed, 1)
	go func() {
		timer, err := reg.arm("s", 30*time.Second)
		done <- armed{timer, err}
	}()
	var got armed
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		require.Fail(t, "arming a timer blocked instead of returning at once")
	}
	require.NoError(t, got.err)
	require.Equal(t, "timer-1", got.timer.info(clock.Now()).ID)

	armedAt := clock.Now()
	info := got.timer.info(armedAt)
	require.Equal(t, TimerArmed, info.State)
	require.Equal(t, 30*time.Second, info.Duration)
	require.Equal(t, armedAt, info.ArmedAt)
	require.Equal(t, armedAt.Add(30*time.Second), info.FiresAt)
	require.Equal(t, 30*time.Second, info.Remaining)
	require.False(t, info.Collected)

	// Ids are monotonic within the session, and the snapshot lists them
	// oldest first.
	second, err := reg.arm("s", 5*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "timer-2", second.info(clock.Now()).ID)

	clock.Advance(10 * time.Second)
	snapshot := reg.timers("s")
	require.Len(t, snapshot, 2)
	require.Equal(t, "timer-1", snapshot[0].ID)
	require.Equal(t, 20*time.Second, snapshot[0].Remaining)
	require.Equal(t, "timer-2", snapshot[1].ID)
	require.Equal(t, 4*time.Minute+50*time.Second, snapshot[1].Remaining)
	require.Equal(t, TimerArmed, snapshot[1].State)
}

// A timer fires once when its duration elapses, and its result is
// collectable exactly once.
func TestTimerFiresOnceAndIsCollectableOnce(t *testing.T) {
	t.Parallel()

	reg, clock := newTestTimerRegistry(t)
	wake := reg.signalChan("s")
	timer, err := reg.arm("s", 30*time.Second)
	require.NoError(t, err)

	clock.Advance(30 * time.Second)
	waitWoken(t, wake)

	info := timer.info(clock.Now())
	require.Equal(t, TimerFired, info.State)
	require.Zero(t, info.Remaining, "a fired timer has no time left to run")
	require.False(t, info.Collected)
	require.Zero(t, reg.armedCount("s"), "a fired timer no longer holds an armed slot")

	require.True(t, reg.collect("s", info.ID))
	require.True(t, timer.isCollected())
	require.False(t, reg.collect("s", info.ID), "a timer's result is handed out once")
	require.True(t, timer.info(clock.Now()).Collected)

	// One shot only: a later tick never fires it again.
	require.False(t, timer.fire(), "a fired timer stays fired")
	require.Equal(t, TimerFired, timer.info(clock.Now()).State)
}

// The duration is bounded: a timer shorter than a second or longer than
// ten minutes is refused with a message naming the bound.
func TestTimerRejectsDurationsOutsideTheBound(t *testing.T) {
	t.Parallel()

	reg, _ := newTestTimerRegistry(t)
	for _, d := range []time.Duration{
		-time.Second,
		0,
		500 * time.Millisecond,
		601 * time.Second,
		2 * time.Hour,
	} {
		_, err := reg.arm("s", d)
		require.Error(t, err, d)
		require.Contains(t, err.Error(), "between 1 and 600 seconds", d)
	}
	require.Zero(t, reg.armedCount("s"), "a refused timer takes no slot")
	require.Empty(t, reg.timers("s"), "a refused timer leaves no record")
}

// The per-session cap is a real bound: the ninth armed timer is refused
// with a message the model can read, and firing one frees a slot.
func TestTimerCapRefusesTheNinth(t *testing.T) {
	t.Parallel()

	reg, clock := newTestTimerRegistry(t)
	for i := range maxSessionTimers {
		_, err := reg.arm("s", time.Duration(i+1)*time.Minute)
		require.NoError(t, err)
	}

	_, err := reg.arm("s", time.Minute)
	require.Error(t, err)
	require.Contains(t, err.Error(), "8")
	require.Contains(t, err.Error(), "before arming another")

	// The cap is per session: another session still has room.
	_, err = reg.arm("other", time.Minute)
	require.NoError(t, err)

	// Firing them one at a time frees the slots: each minute one timer
	// is due, and each fire is one wakeup.
	wake := reg.signalChan("s")
	for range maxSessionTimers {
		clock.Advance(time.Minute)
		wake = waitFire(t, reg, "s", wake)
	}
	require.Zero(t, reg.armedCount("s"))

	next, err := reg.arm("s", time.Minute)
	require.NoError(t, err)
	require.Equal(t, "timer-9", next.info(clock.Now()).ID, "ids stay monotonic past a freed slot")

	reg.cancelSession("s")
	reg.cancelSession("other")
}

// Two sessions cannot see each other's timers: ids are minted per
// session, so the same id names a different timer in each, and another
// session's id is not this session's result.
func TestTimersAreScopedToTheirSession(t *testing.T) {
	t.Parallel()

	reg, clock := newTestTimerRegistry(t)
	first, err := reg.arm("a", 10*time.Second)
	require.NoError(t, err)
	second, err := reg.arm("b", 20*time.Second)
	require.NoError(t, err)

	require.Equal(t, "timer-1", first.info(clock.Now()).ID)
	require.Equal(t, "timer-1", second.info(clock.Now()).ID)
	require.NotSame(t, first, second)

	require.Same(t, first, reg.lookup("a", "timer-1"))
	require.Same(t, second, reg.lookup("b", "timer-1"))
	require.Nil(t, reg.lookup("a", "timer-2"), "a session sees only the ids it minted")
	require.Nil(t, reg.lookup("nobody", "timer-1"))

	wokeA := reg.signalChan("a")
	clock.Advance(10 * time.Second)
	waitWoken(t, wokeA)
	require.Equal(t, TimerFired, reg.timers("a")[0].State)
	require.Equal(t, TimerArmed, reg.timers("b")[0].State, "the other session's timer is untouched")
	require.False(t, reg.collect("b", "timer-1"), "another session's timer is still out of reach")

	cancelled := reg.signalChan("b")
	reg.cancelSession("b")
	waitWoken(t, cancelled)
	require.Equal(t, TimerCancelled, second.info(clock.Now()).State)
}

// Cancelling a session cancels the timers it still has armed, wakes its
// waiters once and keeps the records, so the cancelled result is still
// collectable.
func TestTimerSessionCancelCancelsArmedTimersAndWakes(t *testing.T) {
	t.Parallel()

	reg, clock := newTestTimerRegistry(t)
	running, err := reg.arm("s", time.Minute)
	require.NoError(t, err)
	done, err := reg.arm("s", time.Second)
	require.NoError(t, err)
	fired := reg.signalChan("s")
	clock.Advance(time.Second)
	waitWoken(t, fired)
	require.Equal(t, TimerFired, done.info(clock.Now()).State)

	wake := reg.signalChan("s")
	reg.cancelSession("s")
	waitWoken(t, wake)

	require.Equal(t, TimerCancelled, running.info(clock.Now()).State)
	require.Zero(t, reg.armedCount("s"))
	require.Same(t, running, reg.lookup("s", "timer-1"), "the record outlives the cancel")
	require.True(t, reg.collect("s", "timer-1"))
	require.False(t, reg.collect("s", "timer-1"))

	// The tick the cancelled timer already had pending never fires it.
	require.False(t, running.fire(), "a cancelled timer never fires")
	require.Equal(t, TimerCancelled, running.info(clock.Now()).State)
	require.Equal(t, TimerFired, done.info(clock.Now()).State, "a fired timer is untouched")
}

// An explicit cancel stops an armed timer, keeps its record so the
// cancelled result is still collectable, and wakes the session once; a
// fired or unknown timer cannot be cancelled at all.
func TestTimerCancelKeepsTheRecord(t *testing.T) {
	t.Parallel()

	reg, clock := newTestTimerRegistry(t)
	timer, err := reg.arm("explicit", 45*time.Second)
	require.NoError(t, err)

	wake := reg.signalChan("explicit")
	require.NoError(t, reg.cancel("explicit", "timer-1"))
	select {
	case <-wake:
	default:
		require.Fail(t, "cancelling a timer did not wake its session")
	}
	require.Equal(t, TimerCancelled, timer.info(clock.Now()).State)
	require.Zero(t, reg.armedCount("explicit"))
	require.True(t, reg.collect("explicit", "timer-1"))

	require.ErrorContains(t, reg.cancel("explicit", "timer-1"), "not armed")
	fired, err := reg.arm("explicit", time.Second)
	require.NoError(t, err)
	firedWake := reg.signalChan("explicit")
	clock.Advance(time.Second)
	waitWoken(t, firedWake)
	require.ErrorContains(t, reg.cancel("explicit", fired.info(clock.Now()).ID), "not armed")
	require.ErrorContains(t, reg.cancel("explicit", "timer-99"), "unknown timer")
}

// A fire wakes the session once, and a cancel wakes it once: each is a
// single broadcast on the close-and-replace channel a wait selects on.
func TestTimerBroadcastsOncePerFireAndOncePerCancel(t *testing.T) {
	t.Parallel()

	reg, clock := newTestTimerRegistry(t)

	// One fire, one wakeup.
	wake := reg.signalChan("s")
	_, err := reg.arm("s", 10*time.Second)
	require.NoError(t, err)
	clock.Advance(10 * time.Second)
	waitWoken(t, wake)
	next := reg.signalChan("s")
	select {
	case <-next:
		require.Fail(t, "the channel fired without a second event")
	default:
	}

	// One cancel, one wakeup.
	cancelled, err := reg.arm("s", 10*time.Second)
	require.NoError(t, err)
	require.NoError(t, reg.cancel("s", cancelled.info(clock.Now()).ID))
	select {
	case <-next:
	default:
		require.Fail(t, "cancelling a timer did not wake the session")
	}
	after := reg.signalChan("s")
	select {
	case <-after:
		require.Fail(t, "the channel fired twice for one cancel")
	default:
	}

	// A session cancel broadcasts once however many timers it cancels.
	_, err = reg.arm("s", time.Minute)
	require.NoError(t, err)
	_, err = reg.arm("s", time.Minute)
	require.NoError(t, err)
	reg.cancelSession("s")
	select {
	case <-after:
	default:
		require.Fail(t, "cancelling the session did not wake its waiters")
	}
	last := reg.signalChan("s")
	select {
	case <-last:
		require.Fail(t, "the channel fired twice for one session cancel")
	default:
	}
}
