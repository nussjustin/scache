package scache_test

import (
	"context"
	"testing"

	"github.com/nussjustin/scache"
)

func assertLen(tb testing.TB, want int, lenner interface{ Len() int }) {
	tb.Helper()

	if got := lenner.Len(); want != got {
		tb.Fatalf("wanted len %d, got %d", want, got)
	}
}

func TestLRU(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const size = 3

	c := scache.NewLRU(3)
	if got := c.Size(); size != got {
		t.Fatalf("wanted cache with size %d, got size %d", size, got)
	}

	const key1, key2, key3, key4 = "key1", "key2", "key3", "key4"

	assertCacheMiss(t, c, ctx, key1)
	assertCacheMiss(t, c, ctx, key2)
	assertCacheMiss(t, c, ctx, key3)
	assertCacheMiss(t, c, ctx, key4)
	assertLen(t, 0, c)

	assertCacheSet(t, c, ctx, key1, 1)
	assertCacheGet(t, c, ctx, key1, 1)
	assertCacheMiss(t, c, ctx, key2)
	assertCacheMiss(t, c, ctx, key3)
	assertCacheMiss(t, c, ctx, key4)
	assertLen(t, 1, c)

	assertCacheSet(t, c, ctx, key2, 2)
	assertCacheGet(t, c, ctx, key2, 2)
	assertCacheGet(t, c, ctx, key1, 1)
	assertCacheMiss(t, c, ctx, key3)
	assertCacheMiss(t, c, ctx, key4)
	assertLen(t, 2, c)

	assertCacheSet(t, c, ctx, key3, 3)
	assertCacheGet(t, c, ctx, key3, 3)
	assertCacheGet(t, c, ctx, key1, 1)
	assertCacheGet(t, c, ctx, key2, 2)
	assertCacheMiss(t, c, ctx, key4)
	assertLen(t, 3, c)

	assertCacheSet(t, c, ctx, key4, 4)
	assertCacheGet(t, c, ctx, key4, 4)
	assertCacheMiss(t, c, ctx, key3)
	assertCacheGet(t, c, ctx, key1, 1)
	assertCacheGet(t, c, ctx, key2, 2)
	assertLen(t, 3, c)

	assertCacheSet(t, c, ctx, key4, "3!")
	assertCacheGet(t, c, ctx, key4, "3!")
	assertCacheMiss(t, c, ctx, key3)
	assertCacheGet(t, c, ctx, key1, 1)
	assertCacheGet(t, c, ctx, key2, 2)
	assertLen(t, 3, c)

	assertCacheSet(t, c, ctx, key3, "3!")
	assertCacheGet(t, c, ctx, key3, "3!")
	assertCacheMiss(t, c, ctx, key4)
	assertCacheGet(t, c, ctx, key2, 2)
	assertCacheGet(t, c, ctx, key1, 1)
	assertLen(t, 3, c)

	assertCacheSet(t, c, ctx, key3, "3!!")
	assertCacheSet(t, c, ctx, key4, "4!")
	assertCacheGet(t, c, ctx, key1, 1)
	assertCacheMiss(t, c, ctx, key2)
	assertCacheGet(t, c, ctx, key3, "3!!")
	assertCacheGet(t, c, ctx, key4, "4!")
	assertLen(t, 3, c)
}

func BenchmarkLRU(b *testing.B) {
	b.Run("Get", func(b *testing.B) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		keys := [...]string{
			"key1",
			"key2",
			"key3",
			"key4",
		}

		c := scache.NewLRU(4)
		for _, key := range keys {
			assertCacheSet(b, c, ctx, key, key)
		}

		for i := 0; i < b.N; i++ {
			key := keys[i%len(keys)]

			if _, ok := c.Get(ctx, key); !ok {
				b.Fatalf("failed to get key %q", key)
			}
		}
	})

	b.Run("Set", func(b *testing.B) {
		b.Run("Add", func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			keys := [...]string{
				"key1",
				"key2",
				"key3",
				"key4",
			}

			c := scache.NewLRU(len(keys) / 2)
			for _, key := range keys {
				assertCacheSet(b, c, ctx, key, key)
			}

			b.ResetTimer()

			for i := b.N; i < b.N*2; i++ {
				key := keys[i%len(keys)]

				if err := c.Set(ctx, key, key); err != nil {
					b.Fatalf("failed to set key %q to value %q: %s", key, key, err)
				}
			}
		})

		b.Run("Update", func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			const key = "key"

			c := scache.NewLRU(b.N)

			for i := 0; i < b.N; i++ {
				if err := c.Set(ctx, key, key); err != nil {
					b.Fatalf("failed to set key %q to value %q: %s", key, key, err)
				}
			}
		})
	})
}
