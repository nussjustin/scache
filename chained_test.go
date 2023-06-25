package scache_test

import (
	"context"
	"testing"
	"time"

	"go4.org/mem"

	"github.com/nussjustin/scache"
)

type contextCacheCache[T any] struct{}

func (c contextCacheCache[T]) Get(context.Context, mem.RO) (entry scache.EntryView[T], ok bool) {
	return
}

func (c contextCacheCache[T]) Set(ctx context.Context, _ mem.RO, _ scache.Entry[T]) error {
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
		c1.NowFunc, c2.NowFunc, c3.NowFunc = ft.Now, ft.Now, ft.Now

		assertCacheSet[string](t, c1, ctx, "key1", "val1")
		a1 := ft.Now()

		ft.Add(5 * time.Millisecond)

		assertCacheSet[string](t, c2, ctx, "key2", "val2")
		a2 := ft.Now()

		ft.Add(5 * time.Millisecond)

		assertCacheSet[string](t, c3, ctx, "key3", "val3")
		a3 := ft.Now()

		cc := scache.NewChainedCache[string](c1, c2, c3)

		assertCacheGetWithCreatedAt[string](t, cc, ctx, "key1", "val1", a1)
		assertCacheGetWithCreatedAt[string](t, cc, ctx, "key2", "val2", a2)
		assertCacheGetWithCreatedAt[string](t, cc, ctx, "key3", "val3", a3)

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

	t.Run("GetMany", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c1 := scache.NewLRU[int](3)
		assertCacheSet[int](t, c1, ctx, "key1", 1)

		c2 := scache.NewLRU[int](3)
		assertCacheSet[int](t, c2, ctx, "key1", 10)
		assertCacheSet[int](t, c2, ctx, "key2", 20)

		c3 := scache.NewLRU[int](3)
		assertCacheSet[int](t, c3, ctx, "key1", 100)
		assertCacheSet[int](t, c3, ctx, "key2", 200)
		assertCacheSet[int](t, c3, ctx, "key3", 300)

		cc := scache.NewChainedCache[int](c1, c2, c3).(interface {
			GetMany(context.Context, ...mem.RO) []scache.EntryView[int]
		})

		views := cc.GetMany(ctx, mem.S("key1"), mem.S("key2"), mem.S("key3"), mem.S("key4"))

		assertValues(t, views, []*kvPair[int]{
			{key: "key1", value: 1},
			{key: "key2", value: 20},
			{key: "key3", value: 300},
			nil,
		})
	})

	t.Run("GetMany when empty", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cc := scache.NewChainedCache[int]().(interface {
			GetMany(context.Context, ...mem.RO) []scache.EntryView[int]
		})

		views := cc.GetMany(ctx, mem.S("1"), mem.S("2"), mem.S("3"), mem.S("4"))

		assertValues(t, views, []*kvPair[int]{
			nil,
			nil,
			nil,
			nil,
		})
	})
}

func NewRedisCache[T any]() scache.Cache[T] { return nil }

func ExampleNewChainedCache() {
	cc := scache.NewChainedCache[string](
		scache.NewLRU[string](32),
		NewRedisCache[string](), // external, slower cache
	)

	if err := cc.Set(context.Background(), mem.S("hello"), scache.Value("world")); err != nil {
		panic(err)
	}

	// later...

	entry, ok := cc.Get(context.Background(), mem.S("hello"))
	if ok {
		// do something with the value...
		_ = entry.Value
	}
}
