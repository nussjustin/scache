package scache_test

import (
	"context"
	"testing"
	"time"

	"github.com/nussjustin/scache"
)

type contextCacheCache struct {
}

func (c contextCacheCache) Get(context.Context, string) (val interface{}, age time.Duration, ok bool) {
	return
}

func (c contextCacheCache) Set(ctx context.Context, _ string, _ interface{}) error {
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

		var ft fakeTime

		c1, c2, c3 := scache.NewLRU(4), scache.NewLRU(4), scache.NewLRU(4)
		c1.NowFunc, c2.NowFunc, c3.NowFunc = ft.NowFunc, ft.NowFunc, ft.NowFunc

		assertCacheSet(t, c1, ctx, "key1", "val1")
		ft.Add(5 * time.Millisecond)
		assertCacheSet(t, c2, ctx, "key2", "val2")
		ft.Add(5 * time.Millisecond)
		assertCacheSet(t, c3, ctx, "key3", "val3")
		ft.Add(5 * time.Millisecond)

		cc := scache.NewChainedCache(c1, c2, c3)

		assertCacheGetWithAge(t, cc, ctx, "key1", "val1", 15*time.Millisecond)
		assertCacheGetWithAge(t, cc, ctx, "key2", "val2", 10*time.Millisecond)
		assertCacheGetWithAge(t, cc, ctx, "key3", "val3", 5*time.Millisecond)

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

func NewRedisCache() scache.Cache { return nil }

func ExampleNewChainedCache() {
	cc := scache.NewChainedCache(
		scache.NewLRU(32),
		NewRedisCache(), // external, slower cache
	)

	if err := cc.Set(context.Background(), "hello", "world"); err != nil {
		panic(err)
	}

	// later...

	val, age, ok := cc.Get(context.Background(), "hello")
	if ok {
		// do something with the value...
		_, _ = val, age
	}
}
