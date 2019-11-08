package scache

import (
	"context"
	"sync"
	"time"
)

type LRU struct {
	size int
	ttl  time.Duration

	mu         sync.Mutex
	entries    map[string]*lruItem
	head, tail *lruItem
	free       *lruItem
}

type lruItem struct {
	prev, next *lruItem

	key      string
	val      interface{}
	expireAt time.Time
}

// NewLRU returns a new fixed-size, in-memory Cache implementation that can hold size items.
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
		size:    size,
		ttl:     ttl,
	}
}

func (l *LRU) addLocked(key string, val interface{}, expireAt time.Time) {
	var item *lruItem
	if l.free != nil {
		item, l.free = l.free, nil
		item.key, item.val, item.expireAt = key, val, expireAt
	} else {
		item = &lruItem{key: key, val: val, expireAt: expireAt}
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
	} else if l.tail == nil {
		l.tail = l.head
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

// Get implements the Cache interface.
func (l *LRU) Get(ctx context.Context, key string) (val interface{}, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if item := l.entries[key]; item != nil {
		if item.expireAt.IsZero() || time.Until(item.expireAt) > 0 {
			l.moveToFrontLocked(item)
			return item.val, true
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

// Set implements the Cache interface.
func (l *LRU) Set(ctx context.Context, key string, val interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var expireAt time.Time
	if l.ttl > 0 {
		expireAt = time.Now().Add(l.ttl)
	}

	if item := l.entries[key]; item != nil {
		item.expireAt = expireAt
		item.val = val
		l.moveToFrontLocked(item)
		return nil
	}
	if len(l.entries) >= l.size {
		l.deleteLocked(l.tail)
	}
	l.addLocked(key, val, expireAt)
	return nil
}

// Size returns the maximum number of items that can be cached.
func (l *LRU) Size() int {
	return l.size
}
