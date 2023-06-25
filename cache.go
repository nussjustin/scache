package scache

import (
	"context"
	"time"

	"go4.org/mem"
)

// Cache defines methods for setting and retrieving values in a Cache.
type Cache[T any] interface {
	// Get retrieves the cached value for the given key.
	//
	// The age return value contains the duration for which the value is in the cache.
	Get(ctx context.Context, key mem.RO) (entry EntryView[T], ok bool)

	// Set adds the given view to the cache.
	//
	// If there is already a value with the same key in the Cache, it will be replaced.
	Set(ctx context.Context, key mem.RO, entry Entry[T]) error
}

// Entry defines a new entry to be added to a cache.
type Entry[T any] struct {
	// CreatedAt contains the time at which the entry was created.
	//
	// If the value is empty it will be set automatically when saving the entry.
	CreatedAt time.Time

	// ExpiresAt defines an optional expiration time after which the value should be removed from the cache.
	ExpiresAt time.Time

	// Key is the key used for saving and looking up the entry.
	//
	// This is ignored when saving an entry.
	Key mem.RO

	// Tags is an optional list of tags that can be used by caches for example to offer deleting entries by tag.
	Tags []mem.RO

	// Value contains the value that will be cached.
	Value T

	// Weight is an optional value that can be used by caches to order, prioritize and limit stored entries.
	Weight int
}

// Value returns a new Entry for the given value. This is a shortcut for Entry[T]{Value: v}.
func Value[T any](v T) Entry[T] {
	return Entry[T]{Value: v}
}

// View returns a new EntryView for e with the given key and creation time.
func (e Entry[T]) View() EntryView[T] {
	return EntryView[T]{
		CreatedAt: e.CreatedAt,
		ExpiresAt: e.ExpiresAt,
		Key:       e.Key,
		Value:     e.Value,
		Weight:    e.Weight,
		tags:      e.Tags,
	}
}

// EntryView provides read-only view of a cache entry.
type EntryView[T any] struct {
	// CreatedAt contains the time at which the entry was created.
	CreatedAt time.Time

	// ExpiresAt defines an optional expiration time for the Entry after which it should be deleted.
	ExpiresAt time.Time

	// Key is the key under which the entry was saved.
	Key mem.RO

	// Value contains the cached value.
	Value T

	// Private so that users can not change the slice
	tags []mem.RO

	// Weight is an optional value that can be used by caches to order, prioritize and limit stored entries.
	Weight int
}

// Tags calls f for each tag associated with the view. If f returns false, the method returns.
func (e EntryView[T]) Tags(f func(mem.RO) bool) {
	for i := range e.tags {
		if !f(e.tags[i]) {
			return
		}
	}
}

// TagsCount returns the number of tags associated with the view.
func (e EntryView[T]) TagsCount() int {
	return len(e.tags)
}

// noop implements an always empty cache.
type noop[T any] struct{}

var _ Cache[any] = noop[any]{}

// NewNoopCache returns a Cache that never caches anything.
func NewNoopCache[T any]() Cache[T] {
	return noop[T]{}
}

// Get implements the Cache interface.
func (n noop[T]) Get(context.Context, mem.RO) (entry EntryView[T], ok bool) {
	return
}

// Set implements the Cache interface.
func (n noop[T]) Set(context.Context, mem.RO, Entry[T]) error {
	return nil
}
