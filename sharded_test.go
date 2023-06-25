package scache_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"go4.org/mem"

	"github.com/nussjustin/scache"
)

func TestShardedCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := scache.NewShardedCache[int](0, nil); err == nil {
		t.Fatalf("expected an error when passing a non-positive shard count, got nil")
	}

	const shardN = 4

	shards := make(map[scache.Cache[int]]int, shardN)
	shardByIndex := make(map[int]scache.Cache[int], shardN)

	c, err := scache.NewShardedCache[int](shardN, func(shard int) scache.Cache[int] {
		if len(shards) != shard {
			t.Fatalf("wanted shard index %d, got %d", len(shards), shard)
		}
		c := scache.NewLRU[int](shard + 4)
		shards[c] = shard
		shardByIndex[shard] = c
		return c
	})
	assertNoError(t, err)
	if shardN != len(shards) {
		t.Fatalf("wanted %d shards, got %d", shardN, len(shards))
	}

	for i := 0; i < 256; i++ {
		key := strconv.Itoa(i)

		shard := c.Shard(mem.S(key))
		if shard == nil {
			t.Fatalf("failed to get shard for key %q", key)
		}

		shardIdx, ok := shards[shard]
		if !ok {
			t.Fatalf("got unknown shard %#v for key %q", shard, key)
		} else if shardByIndex[shardIdx] != shard {
			t.Fatalf("wanted shard %#v at index %d, got %#v", shardByIndex[shardIdx], shardIdx, shard)
		}

		assertCacheMiss[int](t, c, ctx, key)
		assertCacheMiss[int](t, c.Shard(mem.S(key)), ctx, key)

		assertCacheSet[int](t, c, ctx, key, i)
		assertCacheGet[int](t, c, ctx, key, i)
		assertCacheGet[int](t, c.Shard(mem.S(key)), ctx, key, i)

		j := i + 1

		assertCacheSet[int](t, c.Shard(mem.S(key)), ctx, key, j)
		assertCacheGet[int](t, c, ctx, key, j)
		assertCacheGet[int](t, c.Shard(mem.S(key)), ctx, key, j)
	}
}

func TestShardedCache_GetMany(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := scache.NewShardedCache[int](4, func(int) scache.Cache[int] {
		return scache.NewLRU[int](100)
	})

	if err != nil {
		t.Fatalf("failed to create shareded cache: %s", err)
	}

	keys := make([]mem.RO, 10)
	expected := make([]*kvPair[int], len(keys))

	for i := range keys {
		key := fmt.Sprintf("key%d", i)
		keys[i] = mem.S(key)
		expected[i] = &kvPair[int]{key: key, value: i}
		assertCacheSet[int](t, c, ctx, key, i)
	}

	// Missing key
	keys = append(keys, mem.S("key999"))
	expected = append(expected, nil)

	views := c.GetMany(ctx, keys...)

	assertValues(t, views, expected)
}

func ExampleNewShardedCache() {
	sc, err := scache.NewShardedCache(64, func(int) scache.Cache[string] {
		return scache.NewLRU[string](32)
	})
	if err != nil {
		panic(err)
	}

	// later...

	entry, ok := sc.Get(context.Background(), mem.S("hello"))
	if ok {
		// do something with the value...
		_ = entry.Value
	}
}
