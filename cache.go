package scache

import (
	"context"
	"errors"
	"time"

	"go4.org/mem"
)

// Cache defines methods for setting and retrieving values in a Cache.
type Cache[T any] interface {
	// Get retrieves the cached value for the given key.
	//
	// The age return value contains the duration for which the value is in the cache.
	Get(ctx context.Context, key mem.RO) (val T, age time.Duration, ok bool)

	// Set adds the given value to the cache.
	//
	// If there is already a value with the same key in the Cache, it will be replaced.
	Set(ctx context.Context, key mem.RO, val T) error
}

// Noop implements an always empty cache.
type Noop[T any] struct{}

var _ Cache[interface{}] = Noop[interface{}]{}

// Get implements the Cache interface.
func (n Noop[T]) Get(context.Context, mem.RO) (val T, age time.Duration, ok bool) {
	return
}

// Set implements the Cache interface.
func (n Noop[T]) Set(context.Context, mem.RO, T) error {
	return nil
}

// ShardedCache implements a Cache that partitions entries into multiple underlying Cache
// instances to reduce contention on each Cache instance and increase scalability.
type ShardedCache[T any] struct {
	shards []Cache[T]
}

var _ Cache[interface{}] = &ShardedCache[interface{}]{}

// ErrInvalidShardsCount is returned by NewShardedCache when specifying an invalid number of shards (< 1).
var ErrInvalidShardsCount = errors.New("invalid shards count")

const minShards = 1

// NewShardedCache returns a new ShardedCache using the given number of shards.
//
// For each shard the factory function will be called with the index of the shard (beginning
// at 0) and the returned Cache will be used for all keys that map to the shard with the
// shard index.
//
// A basic factory could look like this:
//
//     func(int) Cache { return NewLRU(32) }
//
func NewShardedCache[T any](shards int, factory func(shard int) Cache[T]) (*ShardedCache[T], error) {
	if shards < minShards {
		return nil, ErrInvalidShardsCount
	}

	ss := make([]Cache[T], shards)
	for i := range ss {
		ss[i] = factory(i)
	}

	return &ShardedCache[T]{shards: ss}, nil
}

// Get implements the Cache interface.
//
// This is a shorthand for calling
//
//     s.Shard(key).Get(ctx, key)
//
func (s *ShardedCache[T]) Get(ctx context.Context, key mem.RO) (val T, age time.Duration, ok bool) {
	return s.Shard(key).Get(ctx, key)
}

// Set implements the Cache interface.
//
// This is a shorthand for calling
//
//     s.Shard(key).Set(ctx, key, val)
//
func (s *ShardedCache[T]) Set(ctx context.Context, key mem.RO, val T) error {
	return s.Shard(key).Set(ctx, key, val)
}

// Shard returns the underlying Cache used for the given key.
func (s *ShardedCache[T]) Shard(key mem.RO) Cache[T] {
	idx := int(key.MapHash() % uint64(len(s.shards)))
	return s.shards[idx]
}
