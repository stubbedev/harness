package agent

import (
	"sync"
	"time"
)

// runGroup counts goroutines in flight and lets a caller wait for them to
// drain. Unlike sync.WaitGroup, a goroutine may start while another caller
// waits: a dispatch racing a cancel is a normal event, not a misuse.
type runGroup struct {
	mu      sync.Mutex
	n       int
	drained chan struct{}
}

// add records a goroutine about to start.
func (g *runGroup) add() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == 0 {
		g.drained = make(chan struct{})
	}
	g.n++
}

// done records a goroutine that has finished.
func (g *runGroup) done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n--
	if g.n == 0 {
		close(g.drained)
	}
}

// wait blocks until every goroutine recorded so far has finished, or
// timeout passes, and reports whether they all finished.
func (g *runGroup) wait(timeout time.Duration) bool {
	g.mu.Lock()
	if g.n == 0 {
		g.mu.Unlock()
		return true
	}
	drained := g.drained
	g.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-drained:
		return true
	case <-timer.C:
		return false
	}
}
