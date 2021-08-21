package scache_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/nussjustin/scache"
)

type lenner interface{ Len() int }

func assertLen(tb testing.TB, want int, lenner lenner) {
	tb.Helper()

	if got := lenner.Len(); want != got {
		tb.Fatalf("wanted len %d, got %d", want, got)
	}
}

type remover[T any] interface {
	Remove(ctx context.Context, key string) (val T, age time.Duration, ok bool)
}

func assertRemoveWithAge[T any](tb testing.TB, remover remover[T], ctx context.Context, key string, want T, wantAge time.Duration) {
	tb.Helper()

	got, gotAge, ok := remover.Remove(ctx, key)
	if !ok {
		tb.Fatalf("failed to remove key %q", key)
	}

	if !reflect.DeepEqual(want, got) {
		tb.Fatalf("failed to assert value: want %v got %v", want, got)
	}

	if wantAge != gotAge {
		tb.Fatalf("failed to assert gotAge: want at least %s got %s", wantAge, gotAge)
	}
}

func TestLRU(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const size = 3

	var ft fakeTime

	c := scache.NewLRU[interface{}](3)
	c.NowFunc = ft.NowFunc

	if got := c.Cap(); size != got {
		t.Fatalf("wanted cache with cap %d, got cap %d", size, got)
	}

	const key1, key2, key3, key4 = "key1", "key2", "key3", "key4"

	assertCacheMiss[interface{}](t, c, ctx, key1)
	assertCacheMiss[interface{}](t, c, ctx, key2)
	assertCacheMiss[interface{}](t, c, ctx, key3)
	assertCacheMiss[interface{}](t, c, ctx, key4)
	assertLen(t, 0, c)

	assertCacheSet[interface{}](t, c, ctx, key1, 1)
	assertCacheGetWithAge[interface{}](t, c, ctx, key1, 1, 0*time.Millisecond)
	assertCacheMiss[interface{}](t, c, ctx, key2)
	assertCacheMiss[interface{}](t, c, ctx, key3)
	assertCacheMiss[interface{}](t, c, ctx, key4)
	assertLen(t, 1, c)

	ft.Add(1 * time.Millisecond)

	assertCacheSet[interface{}](t, c, ctx, key2, 2)
	assertCacheGetWithAge[interface{}](t, c, ctx, key2, 2, 0*time.Millisecond)
	assertCacheGetWithAge[interface{}](t, c, ctx, key1, 1, 1*time.Millisecond)
	assertCacheMiss[interface{}](t, c, ctx, key3)
	assertCacheMiss[interface{}](t, c, ctx, key4)
	assertLen(t, 2, c)

	ft.Add(5 * time.Millisecond)

	assertCacheSet[interface{}](t, c, ctx, key3, 3)
	assertCacheGetWithAge[interface{}](t, c, ctx, key3, 3, 0*time.Millisecond)
	assertCacheGetWithAge[interface{}](t, c, ctx, key1, 1, 6*time.Millisecond)
	assertCacheGetWithAge[interface{}](t, c, ctx, key2, 2, 5*time.Millisecond)
	assertCacheMiss[interface{}](t, c, ctx, key4)
	assertLen(t, 3, c)

	ft.Add(1 * time.Millisecond)

	assertCacheSet[interface{}](t, c, ctx, key4, 4)
	assertCacheGetWithAge[interface{}](t, c, ctx, key4, 4, 0*time.Millisecond)
	assertCacheMiss[interface{}](t, c, ctx, key3)
	assertCacheGetWithAge[interface{}](t, c, ctx, key1, 1, 7*time.Millisecond)
	assertCacheGetWithAge[interface{}](t, c, ctx, key2, 2, 6*time.Millisecond)
	assertLen(t, 3, c)

	ft.Add(1 * time.Millisecond)

	assertCacheSet[interface{}](t, c, ctx, key4, "3!")
	assertCacheGetWithAge[interface{}](t, c, ctx, key4, "3!", 0*time.Millisecond)
	assertCacheMiss[interface{}](t, c, ctx, key3)
	assertCacheGetWithAge[interface{}](t, c, ctx, key1, 1, 8*time.Millisecond)
	assertCacheGetWithAge[interface{}](t, c, ctx, key2, 2, 7*time.Millisecond)
	assertLen(t, 3, c)

	ft.Add(1 * time.Millisecond)

	assertCacheSet[interface{}](t, c, ctx, key3, "3!")
	assertCacheGetWithAge[interface{}](t, c, ctx, key3, "3!", 0*time.Millisecond)
	assertCacheMiss[interface{}](t, c, ctx, key4)
	assertCacheGetWithAge[interface{}](t, c, ctx, key2, 2, 8*time.Millisecond)
	assertCacheGetWithAge[interface{}](t, c, ctx, key1, 1, 9*time.Millisecond)
	assertLen(t, 3, c)

	ft.Add(1 * time.Millisecond)

	assertCacheSet[interface{}](t, c, ctx, key3, "3!!")
	assertCacheSet[interface{}](t, c, ctx, key4, "4!")
	assertCacheGetWithAge[interface{}](t, c, ctx, key1, 1, 10*time.Millisecond)
	assertCacheMiss[interface{}](t, c, ctx, key2)
	assertCacheGetWithAge[interface{}](t, c, ctx, key3, "3!!", 0*time.Millisecond)
	assertCacheGetWithAge[interface{}](t, c, ctx, key4, "4!", 0*time.Millisecond)
	assertLen(t, 3, c)
}

func TestLRUWithTTL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ft fakeTime

	c := scache.NewLRUWithTTL[string](2, 150*time.Millisecond)
	c.NowFunc = ft.NowFunc

	assertCacheSet[string](t, c, ctx, "hello", "world")
	assertCacheGet[string](t, c, ctx, "hello", "world")
	assertLen(t, 1, c)

	ft.Add(150 * time.Millisecond)

	assertLen(t, 1, c)
	assertCacheMiss[string](t, c, ctx, "hello")

	assertCacheSet[string](t, c, ctx, "hello", "world!")
	assertCacheGet[string](t, c, ctx, "hello", "world!")
	assertLen(t, 1, c)

	ft.Add(50 * time.Millisecond)

	assertCacheSet[string](t, c, ctx, "foo", "bar")
	assertCacheGet[string](t, c, ctx, "foo", "bar")
	assertLen(t, 2, c)

	ft.Add(100 * time.Millisecond)

	assertLen(t, 2, c)
	assertCacheMiss[string](t, c, ctx, "hello")
	assertLen(t, 1, c)
	assertCacheGet[string](t, c, ctx, "foo", "bar")

	ft.Add(50 * time.Millisecond)

	assertLen(t, 1, c)
	assertCacheMiss[string](t, c, ctx, "foo")
	assertLen(t, 0, c)
}

func TestLRU_Remove(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ft fakeTime

	c := scache.NewLRU[int](4)
	c.NowFunc = ft.NowFunc

	assertCacheSet[int](t, c, ctx, "one", 1)
	assertCacheSet[int](t, c, ctx, "two", 2)
	ft.Add(50 * time.Millisecond)
	assertCacheSet[int](t, c, ctx, "three", 3)
	assertCacheSet[int](t, c, ctx, "four", 4)

	assertRemoveWithAge[int](t, c, ctx, "one", 1, 50*time.Millisecond)
	assertCacheMiss[int](t, c, ctx, "one")
	assertLen(t, 3, c)
	assertRemoveWithAge[int](t, c, ctx, "four", 4, 0)
	assertCacheMiss[int](t, c, ctx, "four")
	assertLen(t, 2, c)
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

		c := scache.NewLRU[string](4)
		for _, key := range keys {
			assertCacheSet[string](b, c, ctx, key, key)
		}

		for i := 0; i < b.N; i++ {
			key := keys[i%len(keys)]

			if _, _, ok := c.Get(ctx, key); !ok {
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

			c := scache.NewLRU[string](len(keys) / 2)
			for _, key := range keys {
				assertCacheSet[string](b, c, ctx, key, key)
			}

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
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

			c := scache.NewLRU[string](b.N)

			for i := 0; i < b.N; i++ {
				if err := c.Set(ctx, key, key); err != nil {
					b.Fatalf("failed to set key %q to value %q: %s", key, key, err)
				}
			}
		})
	})
}
