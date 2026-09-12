package csync

import (
	"iter"
	"sync"
	"sync/atomic"
)

// NewVersionedMap creates a new versioned, thread-safe map.
func NewVersionedMap[K comparable, V any]() *VersionedMap[K, V] {
	return &VersionedMap[K, V]{
		m: NewMap[K, V](),
	}
}

// VersionedMap is a thread-safe map that keeps track of its version.
type VersionedMap[K comparable, V any] struct {
	m *Map[K, V]
	v atomic.Uint64

	mu      sync.Mutex
	changed chan struct{}
}

// Get gets the value for the specified key from the map.
func (m *VersionedMap[K, V]) Get(key K) (V, bool) {
	return m.m.Get(key)
}

// Set sets the value for the specified key in the map and increments the version.
func (m *VersionedMap[K, V]) Set(key K, value V) {
	m.m.Set(key, value)
	m.bump()
}

// Del deletes the specified key from the map and increments the version.
func (m *VersionedMap[K, V]) Del(key K) {
	m.m.Del(key)
	m.bump()
}

// bump increments the version and wakes everything waiting on [VersionedMap.Changed].
func (m *VersionedMap[K, V]) bump() {
	m.v.Add(1)
	m.mu.Lock()
	ch := m.changed
	m.changed = nil
	m.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// Changed returns a channel that is closed the next time the map changes. A
// fresh channel is handed out after every change, so a waiter must re-read it
// on each turn of its loop.
//
// Read Changed before reading [VersionedMap.Version]: bump increments the
// version before it closes the channel, so that order guarantees a waiter
// either sees the new version or is woken by the channel it is holding, never
// neither.
func (m *VersionedMap[K, V]) Changed() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.changed == nil {
		m.changed = make(chan struct{})
	}
	return m.changed
}

// Seq2 returns an iter.Seq2 that yields key-value pairs from the map.
func (m *VersionedMap[K, V]) Seq2() iter.Seq2[K, V] {
	return m.m.Seq2()
}

// Copy returns a copy of the inner map.
func (m *VersionedMap[K, V]) Copy() map[K]V {
	return m.m.Copy()
}

// Len returns the number of items in the map.
func (m *VersionedMap[K, V]) Len() int {
	return m.m.Len()
}

// Version returns the current version of the map.
func (m *VersionedMap[K, V]) Version() uint64 {
	return m.v.Load()
}
