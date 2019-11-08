package scache

import (
	"context"
	"fmt"

	"github.com/nussjustin/scache/internal/sharding"
)

// Cache defines methods for setting and retrieving values in a Cache
type Cache interface {
	// Get retrieves the cached value for the given key.
	Get(ctx context.Context, key string) (val interface{}, ok bool)

	// Set adds the given value to the cache.
	//
	// If there is already a value with the same key in the Cache, it will be removed.
	Set(ctx context.Context, key string, val interface{}) error
}

// ShardedCache implements a Cache that partitions entries into multuple underlying Cache
// instances for reduced locking and increased scalability.
type ShardedCache struct {
	hasher sharding.Hasher
	shards []Cache
}

var _ Cache = (*ShardedCache)(nil)

// NewShardedCache returns a new ShardedCache using the given number of shards.
//
// For each shard the factory function will be called with the index of the shard (beginning
// at 0) and the returned Cache will be used for all keys that map to the shard with the
// shard index.
//
// A basic factory could look like this:
//
//     func(int) Cache { return NewLRU(20) }
//
func NewShardedCache(shards int, factory func(shard int) Cache) (*ShardedCache, error) {
	if shards < 1 {
		return nil, fmt.Errorf("invalid shards count %d", shards)
	}

	ss := make([]Cache, shards)
	for i := range ss {
		ss[i] = factory(i)
	}

	return &ShardedCache{hasher: sharding.NewHasher(), shards: ss}, nil
}

// Get implements the Cache interface.
//
// This is a shorthand for calling
//
//     s.Shard(key).Get(ctx, key)
//
func (s *ShardedCache) Get(ctx context.Context, key string) (val interface{}, ok bool) {
	return s.Shard(key).Get(ctx, key)
}

// Set implements the Cache interface.
//
// This is a shorthand for calling
//
//     s.Shard(key).Set(ctx, key, val)
//
func (s *ShardedCache) Set(ctx context.Context, key string, val interface{}) error {
	return s.Shard(key).Set(ctx, key, val)
}

// Shard returns the underlying Cache used for the given key.
func (s *ShardedCache) Shard(key string) Cache {
	idx := int(s.hasher.Hash(key) % uint64(len(s.shards)))
	return s.shards[idx]
}
