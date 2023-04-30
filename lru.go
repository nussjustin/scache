package scache

import (
	"context"
	"math"
	"sync"
	"time"

	"go4.org/mem"
)

// LRU implements a fixed-capacity Cache that will automatically remove the least recently
// used cache item once the cache is full and which also allows for manually removing items
// from the cache.
type LRU[T any] struct {
	// NowFunc if set will be called instead of time.Now when determining the current time
	// and for expiry checks.
	//
	// This is used for testing and must be set before calling any methods on the Cache.
	NowFunc func() time.Time

	cap int

	mu         sync.Mutex
	entries    map[uint64]*lruItem[T]
	head, tail *lruItem[T]
	free       *lruItem[T]
	freeCount  int
}

const lruFreeListLimit = 32

type lruItem[T any] struct {
	prev, next *lruItem[T]

	hash uint64
	view EntryView[T]
}

var _ Cache[any] = &LRU[any]{}

// NewLRU returns a new fixed-cap, in-memory Cache implementation that can hold cap items.
//
// Once the cache is full, adding new items will cause the least recently used items to be
// evicted from the cache.
//
// The returned Cache is safe for concurrent use.
func NewLRU[T any](size int) *LRU[T] {
	return &LRU[T]{
		entries: make(map[uint64]*lruItem[T]),
		cap:     size,
	}
}

func (l *LRU[T]) addLocked(view EntryView[T]) {
	if l.free == nil {
		// Don't allocate full length now to reduce memory usage
		const lengthDiv = 2

		free := make([]lruItem[T], lruFreeListLimit/lengthDiv)
		freeCount := len(free)

		for i := range free[:freeCount-1] {
			free[i].next = &free[i+1]
		}

		l.free = &free[0]
		l.freeCount = freeCount
	}

	item := l.free
	l.free = item.next

	*item = lruItem[T]{
		hash: view.Key.MapHash(),
		view: view,
	}

	item.next = l.head
	if l.head != nil {
		l.head.prev = item
		l.head = item
	} else {
		l.head, l.tail = item, item
	}
	l.entries[item.hash] = item
}

func (l *LRU[T]) deleteLocked(item *lruItem[T]) {
	delete(l.entries, item.hash)
	l.unmountLocked(item)

	if l.freeCount < lruFreeListLimit {
		*item = lruItem[T]{next: l.free}
		l.free = item
		l.freeCount++
	}
}

func (l *LRU[T]) moveToFrontLocked(item *lruItem[T]) {
	if l.head == item {
		return
	}
	l.unmountLocked(item)
	if l.head != nil {
		item.next, l.head.prev = l.head, item
	}
	l.head = item
}

func (l *LRU[T]) unmountLocked(item *lruItem[T]) {
	if l.head == item {
		l.head = item.next
	}
	if l.tail == item {
		l.tail = item.prev
	}
	if item.next != nil {
		item.next.prev = item.prev
	}
	if item.prev != nil {
		item.prev.next = item.next
	}
	item.next, item.prev = nil, nil
}

// Cap returns the maximum number of items that can be cached.
func (l *LRU[T]) Cap() int {
	return l.cap
}

// Get implements the Cache interface.
func (l *LRU[T]) Get(_ context.Context, key mem.RO) (entry EntryView[T], ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if item := l.entries[key.MapHash()]; item != nil {
		if l.until(item.view.ExpiresAt) > 0 {
			l.moveToFrontLocked(item)
			return item.view, true
		}
		l.deleteLocked(item)
	}
	return
}

// Len returns the number of items in the Cache.
func (l *LRU[T]) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.entries)
}

// Remove removes a given key from the cache and returns the old value if the cache contained the key.
func (l *LRU[T]) Remove(_ context.Context, key mem.RO) (entry EntryView[T], ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if item := l.entries[key.MapHash()]; item != nil {
		entry, ok = item.view, true
		l.deleteLocked(item)
	}

	return
}

// Set implements the Cache interface.
func (l *LRU[T]) Set(_ context.Context, key mem.RO, entry Entry[T]) error {
	entry.Key = key

	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = l.now()
	}

	view := entry.View()

	l.mu.Lock()
	defer l.mu.Unlock()

	if item := l.entries[key.MapHash()]; item != nil {
		item.view = view
		l.moveToFrontLocked(item)
		return nil
	}
	if len(l.entries) >= l.cap {
		l.deleteLocked(l.tail)
	}
	l.addLocked(view)
	return nil
}

func (l *LRU[T]) now() time.Time {
	if l.NowFunc != nil {
		return l.NowFunc()
	}
	return time.Now()
}

func (l *LRU[T]) until(t time.Time) time.Duration {
	if t.IsZero() {
		return time.Duration(math.MaxInt64)
	}
	// if we are using time.Now for getting the current time, we can avoid some overhead by calling
	// time.Since directly instead of going through l.NowFunc() and calling .Sub()
	if l.NowFunc == nil {
		return time.Until(t)
	}
	return t.Sub(l.NowFunc())
}
