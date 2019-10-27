package scache_test

import (
	"context"
	"testing"

	"github.com/nussjustin/scache"
)

type contextCacheCache struct {
}

func (c contextCacheCache) Get(ctx context.Context, key string) (val interface{}, ok bool) {
	return
}

func (c contextCacheCache) Set(ctx context.Context, key string, val interface{}) error {
	return ctx.Err()
}

func TestChainedCache(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cc := scache.NewChainedCache()

		assertCacheSet(t, cc, ctx, "key1", "val1")
		assertCacheMiss(t, cc, ctx, "key1")
	})

	t.Run("Multiple", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c1, c2, c3 := scache.NewLRU(4), scache.NewLRU(4), scache.NewLRU(4)

		assertCacheSet(t, c1, ctx, "key1", "val1")
		assertCacheSet(t, c2, ctx, "key2", "val2")
		assertCacheSet(t, c3, ctx, "key3", "val3")

		cc := scache.NewChainedCache(c1, c2, c3)

		assertCacheGet(t, cc, ctx, "key1", "val1")
		assertCacheGet(t, cc, ctx, "key2", "val2")
		assertCacheGet(t, cc, ctx, "key3", "val3")

		assertCacheSet(t, cc, ctx, "key4", "val4")

		assertCacheGet(t, c1, ctx, "key4", "val4")
		assertCacheGet(t, c2, ctx, "key4", "val4")
		assertCacheGet(t, c3, ctx, "key4", "val4")
	})

	t.Run("Set error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c1, c2, c3 := scache.NewLRU(4), &contextCacheCache{}, scache.NewLRU(4)

		cc := scache.NewChainedCache(c1, c2, c3)

		assertCacheSet(t, cc, ctx, "keyA", "valA")
		assertCacheGet(t, c1, ctx, "keyA", "valA")
		assertCacheGet(t, c3, ctx, "keyA", "valA")

		cancel()

		assertCacheSetError(t, cc, ctx, "keyB", "valB", context.Canceled)
		assertCacheGet(t, c1, ctx, "keyA", "valA")
		assertCacheMiss(t, c3, ctx, "keyB")
	})
}
