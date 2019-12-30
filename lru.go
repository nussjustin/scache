package scache

import (
	"context"
	"sync"
	"time"
)

// LRU implements a fixed-capacity Cache that will automatically remove the least recently
// used cache item once the cache is full and which also allows for manually removing items
// from the cache.
type LRU struct {
	// NowFunc if set will be called instead of time.Now when determining the current time
	// for age and expiry checks.
	NowFunc func() time.Time

	cap int
	ttl time.Duration

	mu         sync.Mutex
	entries    map[string]*lruItem
	head, tail *lruItem
	free       *lruItem
}

type lruItem struct {
	prev, next *lruItem

	key string
	val interface{}

	added time.Time
	ttl   time.Duration
}

// NewLRU returns a new fixed-cap, in-memory Cache implementation that can hold cap items.
//
// Once the cache is full, adding new items will cause the least recently used items to be
// evicted from the cache.
//
// The returned Cache is safe for concurrent use.
func NewLRU(size int) *LRU {
	return NewLRUWithTTL(size, 0)
}

// NewLRUWithTTL is the same as NewLRU but also automatically adds a TTL to each key after
// which a value will expire and lead to a Cache miss.
//
// If ttl is <= 0, no TTL is used. This is the same as using NewLRU.
func NewLRUWithTTL(size int, ttl time.Duration) *LRU {
	return &LRU{
		entries: make(map[string]*lruItem),
		cap:     size,
		ttl:     ttl,
	}
}

func (l *LRU) addLocked(key string, val interface{}, added time.Time) {
	var item *lruItem
	if l.free != nil {
		item, l.free = l.free, nil
		item.key, item.val, item.added, item.ttl = key, val, added, l.ttl
	} else {
		item = &lruItem{key: key, val: val, added: added, ttl: l.ttl}
	}

	item.next = l.head
	if l.head != nil {
		l.head.prev = item
		l.head = item
	} else {
		l.head, l.tail = item, item
	}
	l.entries[item.key] = item
}

func (l *LRU) deleteLocked(item *lruItem) {
	delete(l.entries, item.key)
	l.unmountLocked(item)

	item.key, item.val = "", nil
	l.free = item
}

func (l *LRU) moveToFrontLocked(item *lruItem) {
	if l.head == item {
		return
	}
	l.unmountLocked(item)
	if l.head != nil {
		item.next, l.head.prev = l.head, item
	}
	l.head = item
}

func (l *LRU) unmountLocked(item *lruItem) {
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
func (l *LRU) Cap() int {
	return l.cap
}

// Get implements the Cache interface.
func (l *LRU) Get(ctx context.Context, key string) (val interface{}, age time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if item := l.entries[key]; item != nil {
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
func (l *LRU) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.entries)
}

// Remove removes a given key from the cache and returns the old value if the cache contained the key.
func (l *LRU) Remove(ctx context.Context, key string) (val interface{}, age time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if item := l.entries[key]; item != nil {
		age = l.since(item.added)
		if item.ttl <= 0 || item.ttl > age {
			val, ok = item.val, true
		}
		l.deleteLocked(item)
	}
	return
}

// Set implements the Cache interface.
func (l *LRU) Set(ctx context.Context, key string, val interface{}) error {
	var now time.Time
	if l.NowFunc != nil {
		now = l.NowFunc()
	} else {
		now = time.Now()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if item := l.entries[key]; item != nil {
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

func (l *LRU) since(t time.Time) time.Duration {
	// if we are using time.Now for getting the current time, we can avoid some overhead by calling
	// time.Since directly instead of going through l.NowFunc() and calling .Sub()
	if l.NowFunc == nil {
		return time.Since(t)
	}
	return l.NowFunc().Sub(t)
}
