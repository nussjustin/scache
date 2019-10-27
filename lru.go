package scache

import (
	"container/list"
	"context"
	"sync"
)

type LRU struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	list    *list.List
	size    int
}

type lruItem struct {
	key string
	val interface{}
}

// NewLRU returns a new fixed-size, in-memory Cache implementation that can hold size items.
//
// Once the cache is full, adding new items will cause the least recently used items to be
// evicted from the cache.
//
// The returned Cache is safe for concurrent use.
func NewLRU(size int) *LRU {
	return &LRU{
		entries: make(map[string]*list.Element),
		list:    list.New(),
		size:    size,
	}
}

// Get implements the Cache interface.
func (l *LRU) Get(ctx context.Context, key string) (val interface{}, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var ele *list.Element
	if ele, ok = l.entries[key]; ok {
		val = ele.Value.(*lruItem).val
		l.list.MoveToFront(ele)
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

	ele, ok := l.entries[key]
	if ok {
		ele.Value.(*lruItem).val = val
		l.list.MoveToFront(ele)
		return nil
	}
	if len(l.entries) >= l.size {
		ele := l.list.Back()
		delete(l.entries, ele.Value.(*lruItem).key)
		l.list.Remove(ele)
	}
	l.entries[key] = l.list.PushFront(&lruItem{key: key, val: val})
	return nil
}

// Size returns the maximum number of items that can be cached.
func (l *LRU) Size() int {
	return l.size
}
