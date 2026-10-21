package message

import (
	"container/list"
	"slices"
	"sync"
)

const decodedPartsBudget = 64 << 20

type decodedParts struct {
	mu      sync.Mutex
	budget  int
	size    int
	order   *list.List
	entries map[string]*list.Element
}

type decodedEntry struct {
	id    string
	raw   string
	parts []ContentPart
}

func newDecodedParts(budget int) *decodedParts {
	return &decodedParts{
		budget:  budget,
		order:   list.New(),
		entries: make(map[string]*list.Element),
	}
}

func (c *decodedParts) decode(id, raw string) ([]ContentPart, error) {
	if parts, ok := c.lookup(id, raw); ok {
		return slices.Clone(parts), nil
	}
	parts, err := unmarshalParts([]byte(raw))
	if err != nil {
		return nil, err
	}
	c.store(id, raw, parts)
	return slices.Clone(parts), nil
}

func (c *decodedParts) lookup(id, raw string) ([]ContentPart, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[id]
	if !ok {
		return nil, false
	}
	e := el.Value.(*decodedEntry)
	if e.raw != raw {
		return nil, false
	}
	c.order.MoveToFront(el)
	return e.parts, true
}

func (c *decodedParts) store(id, raw string, parts []ContentPart) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(id)
	if len(raw) > c.budget {
		return
	}
	c.entries[id] = c.order.PushFront(&decodedEntry{id: id, raw: raw, parts: parts})
	c.size += len(raw)
	for c.size > c.budget {
		c.removeLocked(c.order.Back().Value.(*decodedEntry).id)
	}
}

func (c *decodedParts) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(id)
}

func (c *decodedParts) removeLocked(id string) {
	el, ok := c.entries[id]
	if !ok {
		return
	}
	c.size -= len(el.Value.(*decodedEntry).raw)
	c.order.Remove(el)
	delete(c.entries, id)
}
