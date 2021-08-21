package scache_test

import (
	"context"
	"testing"
	"time"

	"github.com/nussjustin/scache"
)

type contextCacheCache[T any] struct {
}

func (c contextCacheCache[T]) Get(context.Context, string) (val T, age time.Duration, ok bool) {
	return
}

func (c contextCacheCache[T]) Set(ctx context.Context, _ string, _ T) error {
	return ctx.Err()
}

func TestChainedCache(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cc := scache.NewChainedCache[string]()

		assertCacheSet(t, cc, ctx, "key1", "val1")
		assertCacheMiss(t, cc, ctx, "key1")
	})

	t.Run("Multiple", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var ft fakeTime

		c1, c2, c3 := scache.NewLRU[string](4), scache.NewLRU[string](4), scache.NewLRU[string](4)
		c1.NowFunc, c2.NowFunc, c3.NowFunc = ft.NowFunc, ft.NowFunc, ft.NowFunc

		assertCacheSet[string](t, c1, ctx, "key1", "val1")
		ft.Add(5 * time.Millisecond)
		assertCacheSet[string](t, c2, ctx, "key2", "val2")
		ft.Add(5 * time.Millisecond)
		assertCacheSet[string](t, c3, ctx, "key3", "val3")
		ft.Add(5 * time.Millisecond)

		cc := scache.NewChainedCache[string](c1, c2, c3)

		assertCacheGetWithAge[string](t, cc, ctx, "key1", "val1", 15*time.Millisecond)
		assertCacheGetWithAge[string](t, cc, ctx, "key2", "val2", 10*time.Millisecond)
		assertCacheGetWithAge[string](t, cc, ctx, "key3", "val3", 5*time.Millisecond)

		assertCacheSet[string](t, cc, ctx, "key4", "val4")

		assertCacheGet[string](t, c1, ctx, "key4", "val4")
		assertCacheGet[string](t, c2, ctx, "key4", "val4")
		assertCacheGet[string](t, c3, ctx, "key4", "val4")
	})

	t.Run("Set error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c1, c2, c3 := scache.NewLRU[string](4), &contextCacheCache[string]{}, scache.NewLRU[string](4)

		cc := scache.NewChainedCache[string](c1, c2, c3)

		assertCacheSet[string](t, cc, ctx, "keyA", "valA")
		assertCacheGet[string](t, c1, ctx, "keyA", "valA")
		assertCacheGet[string](t, c3, ctx, "keyA", "valA")

		cancel()

		assertCacheSetError[string](t, cc, ctx, "keyB", "valB", context.Canceled)
		assertCacheGet[string](t, c1, ctx, "keyA", "valA")
		assertCacheMiss[string](t, c3, ctx, "keyB")
	})
}

func NewRedisCache[T any]() scache.Cache[T] { return nil }

func ExampleNewChainedCache() {
	cc := scache.NewChainedCache[string](
		scache.NewLRU[string](32),
		NewRedisCache[string](), // external, slower cache
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
