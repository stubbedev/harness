package csync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVersionedMap_Set(t *testing.T) {
	t.Parallel()

	vm := NewVersionedMap[string, int]()
	require.Equal(t, uint64(0), vm.Version())

	vm.Set("key1", 42)
	require.Equal(t, uint64(1), vm.Version())

	value, ok := vm.Get("key1")
	require.True(t, ok)
	require.Equal(t, 42, value)
}

func TestVersionedMap_Del(t *testing.T) {
	t.Parallel()

	vm := NewVersionedMap[string, int]()
	vm.Set("key1", 42)
	initialVersion := vm.Version()

	vm.Del("key1")
	require.Equal(t, initialVersion+1, vm.Version())

	_, ok := vm.Get("key1")
	require.False(t, ok)
}

func TestVersionedMap_VersionIncrement(t *testing.T) {
	t.Parallel()

	vm := NewVersionedMap[string, int]()
	initialVersion := vm.Version()

	// Setting a value should increment the version
	vm.Set("key1", 42)
	require.Equal(t, initialVersion+1, vm.Version())

	// Deleting a value should increment the version
	vm.Del("key1")
	require.Equal(t, initialVersion+2, vm.Version())

	// Deleting a non-existent key should still increment the version
	vm.Del("nonexistent")
	require.Equal(t, initialVersion+3, vm.Version())
}

func TestVersionedMap_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	vm := NewVersionedMap[int, int]()
	const numGoroutines = 100
	const numOperations = 100

	// Initial version
	initialVersion := vm.Version()

	// Perform concurrent Set and Del operations
	for i := range numGoroutines {
		go func(id int) {
			for j := range numOperations {
				key := id*numOperations + j
				vm.Set(key, key*2)
				vm.Del(key)
			}
		}(i)
	}

	// Wait for operations to complete by checking the version
	// This is a simplified check - in a real test you might want to use sync.WaitGroup
	expectedMinVersion := initialVersion + uint64(numGoroutines*numOperations*2)

	// Allow some time for operations to complete
	for vm.Version() < expectedMinVersion {
		// Busy wait - in a real test you'd use proper synchronization
	}

	// Final version should be at least the expected minimum
	require.GreaterOrEqual(t, vm.Version(), expectedMinVersion)
	require.Equal(t, 0, vm.Len())
}

func TestVersionedMapChangedWakesOnSet(t *testing.T) {
	t.Parallel()

	vm := NewVersionedMap[string, int]()
	changed := vm.Changed()

	select {
	case <-changed:
		t.Fatal("channel closed before any change")
	default:
	}

	go vm.Set("a", 1)

	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("Changed channel was not closed by Set")
	}

	// A fresh channel is handed out after a change.
	next := vm.Changed()
	select {
	case <-next:
		t.Fatal("stale channel returned after a change")
	default:
	}

	go vm.Del("a")
	select {
	case <-next:
	case <-time.After(time.Second):
		t.Fatal("Changed channel was not closed by Del")
	}
}

func TestVersionedMapChangedNoMissedWakeup(t *testing.T) {
	t.Parallel()

	// The documented ordering — read Changed, then Version — must never let a
	// waiter miss a change that lands between the two reads.
	for range 200 {
		vm := NewVersionedMap[string, int]()
		before := vm.Version()

		start := make(chan struct{})
		go func() {
			<-start
			vm.Set("a", 1)
		}()

		close(start)
		changed := vm.Changed()
		if vm.Version() != before {
			continue // Saw the new version directly.
		}
		select {
		case <-changed:
		case <-time.After(time.Second):
			t.Fatal("waiter saw neither the new version nor a wakeup")
		}
	}
}
