package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunGroupWaitsForEveryRunInFlight(t *testing.T) {
	t.Parallel()

	var g runGroup
	assert.True(t, g.wait(time.Millisecond), "an idle group has nothing to wait for")

	g.add()
	g.add()
	waited := make(chan bool, 1)
	go func() { waited <- g.wait(5 * time.Second) }()

	g.done()
	select {
	case <-waited:
		t.Fatal("the wait returned with a run still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	g.done()
	require.True(t, <-waited, "the wait returns once the last run finishes")
}

func TestRunGroupWaitGivesUpAtItsTimeout(t *testing.T) {
	t.Parallel()

	var g runGroup
	g.add()
	start := time.Now()
	assert.False(t, g.wait(20*time.Millisecond), "a run stuck past the timeout is not waited for")
	assert.Less(t, time.Since(start), 5*time.Second)
	g.done()
	g.add()
	g.done()
	assert.True(t, g.wait(time.Millisecond), "the group is reusable after draining")
}
