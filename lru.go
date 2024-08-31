package scache

import (
	"context"
	"sync"
	"time"
)

// LRU implements an in-memory [Adapter] using an implementation of an LRU algorithm.
//
// Expired items are not automatically removed.
//
// TODO: Use maphash.Comparable in Go 1.24 and allow the key to be configured.
type LRU[K comparable, V any] struct {
	mu         sync.Mutex
	m          map[string]*lruItem[K, V]
	head, tail *lruItem[K, V]
	cap        uint
}

type lruItem[K comparable, V any] struct {
	key        string
	value      V
	time       time.Time
	prev, next *lruItem[K, V]
}

// NewLRU returns an in-memory cache [Adapter] using an implementation of an LRU algorithm.
func NewLRU[K comparable, V any](size uint) *LRU[K, V] {
	return &LRU[K, V]{m: make(map[string]*lruItem[K, V]), cap: size}
}

// Delete removes the entry with the given key from the cache.
func (l *LRU[K, V]) Delete(ctx context.Context, key string) error {
	_ = ctx

	l.mu.Lock()
	defer l.mu.Unlock()

	item := l.m[key]
	if item == nil {
		return nil
	}

	l.removeLocked(item)
	return nil
}

// Get implements the [Backend] interface.
func (l *LRU[K, V]) Get(ctx context.Context, key string) (value V, age time.Duration, err error) {
	_ = ctx

	l.mu.Lock()
	defer l.mu.Unlock()

	item := l.m[key]
	if item == nil {
		var zero V
		return zero, 0, ErrNotFound
	}

	l.detachLocked(item)
	l.attachLocked(item)

	return item.value, time.Since(item.time), nil
}

// Len returns the number of entries currently in the cache.
func (l *LRU[K, V]) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

// Set implements the [Backend] interface.
//
// If the weight of the value is greater than the size of the LRU, the value will be discarded.
func (l *LRU[K, V]) Set(ctx context.Context, key string, value V) error {
	_ = ctx

	l.mu.Lock()
	defer l.mu.Unlock()

	var newItem *lruItem[K, V]

	if oldItem := l.m[key]; oldItem != nil {
		l.removeLocked(oldItem)
		newItem = oldItem
	}

	// guaranteed to finish since we checked that our value fits into the total capacity
	for uint(len(l.m)) >= l.cap {
		tail := l.tail
		l.removeLocked(tail)
		newItem = tail
	}

	if newItem == nil {
		newItem = &lruItem[K, V]{}
	}

	*newItem = lruItem[K, V]{key: key, value: value, time: time.Now()}

	l.addLocked(newItem)

	return nil
}

func (l *LRU[K, V]) addLocked(item *lruItem[K, V]) {
	l.attachLocked(item)
	l.m[item.key] = item
}

func (l *LRU[K, V]) removeLocked(item *lruItem[K, V]) {
	l.detachLocked(item)
	delete(l.m, item.key)
}

func (l *LRU[K, V]) attachLocked(item *lruItem[K, V]) {
	item.prev = nil
	item.next = l.head

	if l.head != nil {
		l.head.prev = item
	} else {
		l.tail = item
	}

	l.head = item
}

func (l *LRU[K, V]) detachLocked(item *lruItem[K, V]) {
	if l.head == item {
		l.head = item.next
	}

	if l.tail == item {
		l.tail = item.prev
	}

	if item.prev != nil {
		item.prev.next = item.next
	}

	if item.next != nil {
		item.next.prev = item.prev
	}
}
