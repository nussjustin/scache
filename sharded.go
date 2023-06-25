package scache

import (
	"context"
	"errors"

	"go4.org/mem"
)

// ShardedCache implements a Cache that partitions entries into multiple underlying Cache
// instances to reduce contention on each Cache instance and increase scalability.
type ShardedCache[T any] struct {
	shards []Cache[T]
}

var _ Cache[any] = &ShardedCache[any]{}

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
//	func(int) Cache { return NewLRU(32) }
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
//	s.Shard(key).Get(ctx, key)
func (s *ShardedCache[T]) Get(ctx context.Context, key mem.RO) (entry EntryView[T], ok bool) {
	return s.Shard(key).Get(ctx, key)
}

// GetMany returns multiple entries at once. See the global [GetMany] function for more details.
func (s *ShardedCache[T]) GetMany(ctx context.Context, keys ...mem.RO) []EntryView[T] {
	preAlloc := 4
	if preAlloc > len(keys) {
		preAlloc = len(keys)
	}

	shards := make(map[int][]mem.RO, preAlloc)
	keyIdxByHash := make(map[uint64]int, len(keys))

	for i, key := range keys {
		hash := key.MapHash()
		shardIdx := int(hash % uint64(len(s.shards)))

		shards[shardIdx] = append(shards[shardIdx], key)
		keyIdxByHash[hash] = i
	}

	entries := make([]EntryView[T], len(keys))

	for shardIdx, shardKeys := range shards {
		for _, entry := range GetMany[T](ctx, s.shards[shardIdx], shardKeys...) {
			if !entry.CreatedAt.IsZero() {
				entries[keyIdxByHash[entry.Key.MapHash()]] = entry
			}
		}
	}

	return entries
}

// Set implements the Cache interface.
//
// This is a shorthand for calling
//
//	s.Shard(key).Set(ctx, key, val)
func (s *ShardedCache[T]) Set(ctx context.Context, key mem.RO, entry Entry[T]) error {
	return s.Shard(key).Set(ctx, key, entry)
}

// Shard returns the underlying Cache used for the given key.
func (s *ShardedCache[T]) Shard(key mem.RO) Cache[T] {
	idx := int(key.MapHash() % uint64(len(s.shards)))
	return s.shards[idx]
}
