package scache

import (
	"container/list"
	"context"
	"sync"
	"time"
)

type LRU struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	list    *list.List
	size    int
	ttl     time.Duration
}

type lruItem struct {
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
		entries: make(map[string]*list.Element),
		list:    list.New(),
		size:    size,
		ttl: ttl,
	}
}

func (l *LRU) deleteLocked(ele *list.Element) {
	delete(l.entries, ele.Value.(*lruItem).key)
	l.list.Remove(ele)
}

// Get implements the Cache interface.
func (l *LRU) Get(ctx context.Context, key string) (val interface{}, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var ele *list.Element
	if ele = l.entries[key]; ele != nil {
		item := ele.Value.(*lruItem)
		if item.expireAt.IsZero() || time.Until(item.expireAt) > 0 {
			l.list.MoveToFront(ele)
			return ele.Value.(*lruItem).val, true
		}
		l.deleteLocked(ele)
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

	ele, ok := l.entries[key]
	if ok {
		*ele.Value.(*lruItem) = lruItem{key: key, val: val, expireAt: expireAt}
		l.list.MoveToFront(ele)
		return nil
	}
	if len(l.entries) >= l.size {
		l.deleteLocked(l.list.Back())
	}
	l.entries[key] = l.list.PushFront(&lruItem{key: key, val: val, expireAt: expireAt})
	return nil
}

// Size returns the maximum number of items that can be cached.
func (l *LRU) Size() int {
	return l.size
}
