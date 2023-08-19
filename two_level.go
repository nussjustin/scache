package scache

import (
	"context"
	"time"
)

type twoLevelBackend[T any] struct {
	l1, l2 Backend[T]
}

// NewTwoLevelBackend returns a backend that layers an existing backend on top of another backend.
//
// When retrieving values from the cache, first the first level cache will be consulted. If the
// first level cache does not have a value for a key or the value is expired, the second level is
// consulted.
//
// New values are set in the second level cache. When a value is fetched from the second level cache
// and that value has not yet expired, it will be saved into the first level cache for faster lookups.
//
// Errors when fetching from the first level cache are ignored and cause a lookup from the second
// level cache. Similarly, errors when saving into the first level cache are ignored.
func NewTwoLevelBackend[T any](firstLevel, secondLevel Backend[T]) Backend[T] {
	return &twoLevelBackend[T]{l1: firstLevel, l2: secondLevel}
}

func (t twoLevelBackend[T]) Get(ctx context.Context, key string) (Item[T], error) {
	// We ignore the error since we want to fall back to the second level on error.
	item, err := t.l1.Get(ctx, key)
	if err == nil && item.Hit && (item.ExpiresAt.IsZero() || time.Until(item.ExpiresAt) > 0) {
		return item, nil
	}

	item, err = t.l2.Get(ctx, key)
	if err == nil && item.Hit && (item.ExpiresAt.IsZero() || time.Until(item.ExpiresAt) > 0) {
		// Again we ignore the error since we already have an item
		_ = t.l1.Set(ctx, key, item)

		return item, nil
	}

	return Item[T]{}, err
}

func (t twoLevelBackend[T]) Set(ctx context.Context, key string, item Item[T]) error {
	return t.l2.Set(ctx, key, item)
}
