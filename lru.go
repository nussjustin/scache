package scache

import (
	"context"
	"sync"
	"time"
)

// LRU implements an in-memory cache [Backend] using an implementation of an LRU algorithm.
//
// The implementation supports weights.
//
// Expired items are not automatically removed.
type LRU[T any] struct {
	mu          sync.Mutex
	m           map[string]*lruItem[T]
	head, tail  *lruItem[T]
	cap, weight uint
}

type lruItem[T any] struct {
	key        string
	item       Item[T]
	prev, next *lruItem[T]
}

// NewLRU returns an in-memory cache [Backend] using an implementation of an LRU algorithm.
func NewLRU[T any](size uint) *LRU[T] {
	return &LRU[T]{m: make(map[string]*lruItem[T]), cap: size}
}

// Delete removes the entry with the given key from the cache.
func (l *LRU[T]) Delete(ctx context.Context, key string) error {
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
func (l *LRU[T]) Get(ctx context.Context, key string) (Item[T], error) {
	_ = ctx

	l.mu.Lock()
	defer l.mu.Unlock()

	item := l.m[key]
	if item == nil {
		return Item[T]{}, nil
	}

	l.detachLocked(item)
	l.attachLocked(item)

	return item.item, nil
}

// Len returns the number of entries currently in the cache.
func (l *LRU[T]) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

// Set implements the [Backend] interface.
//
// If the weight of the item is greater than the size of the LRU, the item will be discarded.
func (l *LRU[T]) Set(ctx context.Context, key string, item Item[T]) error {
	_ = ctx

	item.Weight = max(1, item.Weight)

	if item.Weight > l.cap {
		return nil
	}

	item.CachedAt = time.Now()
	item.Hit = true

	l.mu.Lock()
	defer l.mu.Unlock()

	var newItem *lruItem[T]

	if oldItem := l.m[key]; oldItem != nil {
		l.removeLocked(oldItem)
		newItem = oldItem
	}

	// guaranteed to finish since we checked that our item fits into the total capacity
	for l.weight+item.Weight > l.cap {
		tail := l.tail
		l.removeLocked(tail)
		newItem = tail
	}

	if newItem == nil {
		newItem = &lruItem[T]{}
	}

	*newItem = lruItem[T]{key: key, item: item}

	l.addLocked(newItem)

	return nil
}

func (l *LRU[T]) addLocked(item *lruItem[T]) {
	l.attachLocked(item)
	l.weight += item.item.Weight
	l.m[item.key] = item
}

func (l *LRU[T]) removeLocked(item *lruItem[T]) {
	l.detachLocked(item)
	l.weight -= item.item.Weight
	delete(l.m, item.key)
}

func (l *LRU[T]) attachLocked(item *lruItem[T]) {
	item.prev = nil
	item.next = l.head

	if l.head != nil {
		l.head.prev = item
	} else {
		l.tail = item
	}

	l.head = item
}

func (l *LRU[T]) detachLocked(item *lruItem[T]) {
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
