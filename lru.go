package scache

import (
	"context"
	"sync"
	"time"

	"go4.org/mem"
)

// LRU implements a fixed-capacity Cache that will automatically remove the least recently
// used cache item once the cache is full and which also allows for manually removing items
// from the cache.
type LRU[T any] struct {
	// NowFunc if set will be called instead of time.Now when determining the current time
	// for age and expiry checks.
	NowFunc func() time.Time

	cap int
	ttl time.Duration

	mu         sync.Mutex
	entries    map[uint64]*lruItem[T]
	head, tail *lruItem[T]
	free       *lruItem[T]
	freeCount  int
}

const lruFreeListLimit = 32

type lruItem[T any] struct {
	prev, next *lruItem[T]

	key     mem.RO
	keyHash uint64
	val     T

	added time.Time
	ttl   time.Duration
}

// NewLRU returns a new fixed-cap, in-memory Cache implementation that can hold cap items.
//
// Once the cache is full, adding new items will cause the least recently used items to be
// evicted from the cache.
//
// The returned Cache is safe for concurrent use.
func NewLRU[T any](size int) *LRU[T] {
	return NewLRUWithTTL[T](size, 0)
}

// NewLRUWithTTL is the same as NewLRU but also automatically adds a TTL to each key after
// which a value will expire and lead to a Cache miss.
//
// If ttl is <= 0, no TTL is used. This is the same as using NewLRU.
func NewLRUWithTTL[T any](size int, ttl time.Duration) *LRU[T] {
	return &LRU[T]{
		entries: make(map[uint64]*lruItem[T]),
		cap:     size,
		ttl:     ttl,
	}
}

func (l *LRU[T]) addLocked(key mem.RO, val T, added time.Time) {
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
		key:     key,
		keyHash: key.MapHash(),
		val:     val,
		added:   added,
		ttl:     l.ttl,
	}

	item.next = l.head
	if l.head != nil {
		l.head.prev = item
		l.head = item
	} else {
		l.head, l.tail = item, item
	}
	l.entries[item.keyHash] = item
}

func (l *LRU[T]) deleteLocked(item *lruItem[T]) {
	delete(l.entries, item.keyHash)
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
func (l *LRU[T]) Get(ctx context.Context, key mem.RO) (val T, age time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if item := l.entries[key.MapHash()]; item != nil {
		age = l.since(item.added)
		if item.ttl <= 0 || item.ttl > age {
			l.moveToFrontLocked(item)
			return item.val, age, true
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
func (l *LRU[T]) Remove(ctx context.Context, key mem.RO) (val T, age time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if item := l.entries[key.MapHash()]; item != nil {
		age = l.since(item.added)
		if item.ttl <= 0 || item.ttl > age {
			val, ok = item.val, true
		}
		l.deleteLocked(item)
	}
	return
}

// Set implements the Cache interface.
func (l *LRU[T]) Set(ctx context.Context, key mem.RO, val T) error {
	var now time.Time
	if l.NowFunc != nil {
		now = l.NowFunc()
	} else {
		now = time.Now()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if item := l.entries[key.MapHash()]; item != nil {
		item.added = now
		item.val = val
		l.moveToFrontLocked(item)
		return nil
	}
	if len(l.entries) >= l.cap {
		l.deleteLocked(l.tail)
	}
	l.addLocked(key, val, now)
	return nil
}

func (l *LRU[T]) since(t time.Time) time.Duration {
	// if we are using time.Now for getting the current time, we can avoid some overhead by calling
	// time.Since directly instead of going through l.NowFunc() and calling .Sub()
	if l.NowFunc == nil {
		return time.Since(t)
	}
	return l.NowFunc().Sub(t)
}
