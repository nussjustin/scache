package scache_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/nussjustin/scache"
)

func assertCacheGet(tb testing.TB, c scache.Cache, ctx context.Context, key string, want interface{}) {
	tb.Helper()

	got, ok := c.Get(ctx, key)
	if !ok {
		tb.Fatalf("failed to get key %q", key)
	}

	if !reflect.DeepEqual(want, got) {
		tb.Fatalf("failed to assert value: want %v got %v", want, got)
	}
}

func assertCacheMiss(tb testing.TB, c scache.Cache, ctx context.Context, key string) {
	tb.Helper()

	if val, ok := c.Get(ctx, key); ok {
		tb.Fatalf("failed to assert cache miss: got value %v", val)
	}
}

func assertCacheSet(tb testing.TB, c scache.Cache, ctx context.Context, key string, val interface{}) {
	tb.Helper()

	if err := c.Set(ctx, key, val); err != nil {
		tb.Fatalf("failed to set key %q to value %q: %s", key, val, err)
	}
}

func assertCacheSetError(tb testing.TB, c scache.Cache, ctx context.Context, key string, val interface{}, werr error) {
	tb.Helper()

	if err := c.Set(ctx, key, val); err == nil {
		tb.Fatalf("failed to assert error for key %q: got value %v", key, val)
	} else if !errors.Is(err, werr) {
		tb.Fatalf("failed to assert error for key %q: want %q got %q", key, werr, err)
	}
}

func assertNoError(tb testing.TB, err error) {
	tb.Helper()

	if err != nil {
		tb.Fatalf("failed to assert no error: got %v", err)
	}
}

func TestLockedCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const keysN = 64

	c := scache.NewLRU(keysN)

	var wg sync.WaitGroup
	for i := 0; i < keysN; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			assertCacheSet(t, c, ctx, strconv.Itoa(i), strconv.Itoa(i))
		}(i)
	}
	wg.Wait()

	if n := c.Len(); keysN != n {
		t.Fatalf("wanted %d entries in cache, got %d", keysN, n)
	}

	for i := 0; i < keysN; i++ {
		assertCacheGet(t, c, ctx, strconv.Itoa(i), strconv.Itoa(i))
	}
}

func TestShardedCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const shardN = 4

	shards := make(map[scache.Cache]int, shardN)
	shardByIndex := make(map[int]scache.Cache, shardN)

	c, err := scache.NewShardedCache(shardN, func(shard int) scache.Cache {
		if len(shards) != shard {
			t.Fatalf("wanted shard index %d, got %d", len(shards), shard)
		}
		c := scache.NewLRU(shard + 4)
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

		shard := c.Shard(key)
		if shard == nil {
			t.Fatalf("failed to get shard for key %q", key)
		}

		shardIdx, ok := shards[shard]
		if !ok {
			t.Fatalf("got unknown shard %#v for key %q", shard, key)
		} else if shardByIndex[shardIdx] != shard {
			t.Fatalf("wanted shard %#v at index %d, got %#v", shardByIndex[shardIdx], shardIdx, shard)
		}

		assertCacheMiss(t, c, ctx, key)
		assertCacheMiss(t, c.Shard(key), ctx, key)

		assertCacheSet(t, c, ctx, key, i)
		assertCacheGet(t, c, ctx, key, i)
		assertCacheGet(t, c.Shard(key), ctx, key, i)

		j := i + 1

		assertCacheSet(t, c.Shard(key), ctx, key, j)
		assertCacheGet(t, c, ctx, key, j)
		assertCacheGet(t, c.Shard(key), ctx, key, j)
	}
}
