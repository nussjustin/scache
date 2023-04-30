package scache_test

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"testing"
	"time"

	"go4.org/mem"

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
	Remove(ctx context.Context, key mem.RO) (entry scache.EntryView[T], ok bool)
}

func assertRemove[T any](tb testing.TB, remover remover[T], ctx context.Context, key string, want T) {
	tb.Helper()

	got, ok := remover.Remove(ctx, mem.S(key))
	if !ok {
		tb.Fatalf("failed to remove key %q", key)
	}

	if !reflect.DeepEqual(want, got.Value) {
		tb.Fatalf("failed to assert value: want %v got %v", want, got)
	}
}

func TestLRU(t *testing.T) {
	t.Run("Eviction", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		const size = 3

		var ft fakeTime

		c := scache.NewLRU[any](3)
		c.NowFunc = ft.Now

		if got := c.Cap(); size != got {
			t.Fatalf("wanted cache with cap %d, got cap %d", size, got)
		}

		key1, key2, key3, key4 := "key1", "key2", "key3", "key4"

		t0 := ft.Now()

		assertCacheMiss[any](t, c, ctx, key1)
		assertCacheMiss[any](t, c, ctx, key2)
		assertCacheMiss[any](t, c, ctx, key3)
		assertCacheMiss[any](t, c, ctx, key4)
		assertLen(t, 0, c)

		assertCacheSet[any](t, c, ctx, key1, 1)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key1, 1, t0)
		assertCacheMiss[any](t, c, ctx, key2)
		assertCacheMiss[any](t, c, ctx, key3)
		assertCacheMiss[any](t, c, ctx, key4)
		assertLen(t, 1, c)

		ft.Add(1 * time.Millisecond)

		t1 := ft.Now().Add(time.Second)

		assertCacheSetEntry[any](t, c, ctx, key2, scache.Entry[any]{CreatedAt: t1, Value: 2})
		assertCacheGetWithCreatedAt[any](t, c, ctx, key2, 2, t1)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key1, 1, t0)
		assertCacheMiss[any](t, c, ctx, key3)
		assertCacheMiss[any](t, c, ctx, key4)
		assertLen(t, 2, c)

		ft.Add(5 * time.Millisecond)

		assertCacheSet[any](t, c, ctx, key3, 3)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key3, 3, ft.Now())
		assertCacheGetWithCreatedAt[any](t, c, ctx, key1, 1, t0)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key2, 2, t1)
		assertCacheMiss[any](t, c, ctx, key4)
		assertLen(t, 3, c)

		ft.Add(1 * time.Millisecond)

		assertCacheSet[any](t, c, ctx, key4, 4)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key4, 4, ft.Now())
		assertCacheMiss[any](t, c, ctx, key3)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key1, 1, t0)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key2, 2, t1)
		assertLen(t, 3, c)

		ft.Add(1 * time.Millisecond)

		assertCacheSet[any](t, c, ctx, key4, "3!")
		assertCacheGetWithCreatedAt[any](t, c, ctx, key4, "3!", ft.Now())
		assertCacheMiss[any](t, c, ctx, key3)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key1, 1, t0)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key2, 2, t1)
		assertLen(t, 3, c)

		ft.Add(1 * time.Millisecond)

		assertCacheSet[any](t, c, ctx, key3, "3!")
		assertCacheGetWithCreatedAt[any](t, c, ctx, key3, "3!", ft.Now())
		assertCacheMiss[any](t, c, ctx, key4)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key2, 2, t1)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key1, 1, t0)
		assertLen(t, 3, c)

		ft.Add(1 * time.Millisecond)

		assertCacheSet[any](t, c, ctx, key3, "3!!")
		assertCacheSet[any](t, c, ctx, key4, "4!")
		assertCacheGetWithCreatedAt[any](t, c, ctx, key1, 1, t0)
		assertCacheMiss[any](t, c, ctx, key2)
		assertCacheGetWithCreatedAt[any](t, c, ctx, key3, "3!!", ft.Now())
		assertCacheGetWithCreatedAt[any](t, c, ctx, key4, "4!", ft.Now())
		assertLen(t, 3, c)
	})

	t.Run("Expiry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		const ttl = 150 * time.Millisecond

		var ft fakeTime

		value := func(s string) scache.Entry[string] {
			entry := scache.Value(s)
			entry.ExpiresAt = ft.Now().Add(ttl)
			return entry
		}

		c := scache.NewLRU[string](2)
		c.NowFunc = ft.Now

		assertCacheSetEntry[string](t, c, ctx, "hello", value("world"))
		assertCacheGet[string](t, c, ctx, "hello", "world")
		assertLen(t, 1, c)

		ft.Add(150 * time.Millisecond)

		assertLen(t, 1, c)
		assertCacheMiss[string](t, c, ctx, "hello")

		assertCacheSetEntry[string](t, c, ctx, "hello", value("world!"))
		assertCacheGet[string](t, c, ctx, "hello", "world!")
		assertLen(t, 1, c)

		ft.Add(50 * time.Millisecond)

		assertCacheSetEntry[string](t, c, ctx, "foo", value("bar"))
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
	})
}

func TestLRU_Remove(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ft fakeTime

	c := scache.NewLRU[int](4)
	c.NowFunc = ft.Now

	assertCacheSet[int](t, c, ctx, "one", 1)
	assertCacheSet[int](t, c, ctx, "two", 2)
	ft.Add(50 * time.Millisecond)
	assertCacheSet[int](t, c, ctx, "three", 3)
	assertCacheSet[int](t, c, ctx, "four", 4)

	assertRemove[int](t, c, ctx, "one", 1)
	assertCacheMiss[int](t, c, ctx, "one")
	assertLen(t, 3, c)
	assertRemove[int](t, c, ctx, "four", 4)
	assertCacheMiss[int](t, c, ctx, "four")
	assertLen(t, 2, c)
}

func BenchmarkLRU(b *testing.B) {
	const m = 1000

	keys := make([]string, m)
	for i := range keys {
		keys[i] = strconv.Itoa(i)
	}

	b.Run("Get-1000", func(b *testing.B) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c := scache.NewLRU[string](len(keys))
		for _, key := range keys {
			assertCacheSet[string](b, c, ctx, key, key)
		}

		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			for _, key := range keys {
				if _, ok := c.Get(ctx, mem.S(key)); !ok {
					b.Fatalf("failed to get key %q", key)
				}
			}
		}
	})

	b.Run("Set-1000", func(b *testing.B) {
		b.Run("Add", func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				c := scache.NewLRU[string](len(keys))

				for _, key := range keys {
					if err := c.Set(ctx, mem.S(key), scache.Value(key)); err != nil {
						b.Fatalf("failed to set key %q to value %q: %s", key, key, err)
					}
				}
			}
		})

		b.Run("Mixed", func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			for i := 0; i < b.N; i++ {
				c := scache.NewLRU[string](len(keys))

				b.StopTimer()
				for j, key := range keys {
					if j%2 == 0 {
						assertCacheSet[string](b, c, ctx, key, key)
					}
				}
				b.StartTimer()

				for _, key := range keys {
					if err := c.Set(ctx, mem.S(key), scache.Value(key)); err != nil {
						b.Fatalf("failed to set key %q to value %q: %s", key, key, err)
					}
				}
			}
		})

		b.Run("Update", func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			c := scache.NewLRU[string](b.N)

			for _, key := range keys {
				assertCacheSet[string](b, c, ctx, key, key)
			}

			for i := 0; i < b.N; i++ {
				for _, key := range keys {
					if err := c.Set(ctx, mem.S(key), scache.Value(key)); err != nil {
						b.Fatalf("failed to set key %q to value %q: %s", key, key, err)
					}
				}
			}
		})
	})
}

func getUserID(ctx context.Context) int {
	return 1
}

func ExampleLRU() {
	ctx := context.TODO()

	type User struct {
		ID   int
		Name string
		Age  int
	}

	user := User{ID: 1, Name: "Gopher", Age: 12}

	lru := scache.NewLRU[User](32)

	if err := lru.Set(ctx, mem.S(strconv.Itoa(user.ID)), scache.Value(user)); err != nil {
		log.Fatalf("failed to cache user %d: %s", user.ID, err)
	}

	// ... later

	userID := getUserID(ctx)

	// Ignore age of cache view
	entry, ok := lru.Get(ctx, mem.S(strconv.Itoa(userID)))
	if !ok {
		log.Fatalf("user with ID %d not found in cache", userID)
	}

	fmt.Printf("%+v\n", entry.Value)

	// Output:
	//
	// {ID:1 Name:Gopher Age:12}
}

func getUserIDs(ctx context.Context) []int {
	return []int{1, 2, 3, 4, 5}
}

func ExampleLRU_overflow() {
	ctx := context.TODO()

	type User struct {
		ID   int
		Name string
		Age  int
	}

	lru := scache.NewLRU[User](3)

	users := []User{
		{ID: 1, Name: "Blue Gopher", Age: 12},
		{ID: 2, Name: "Green Gopher", Age: 7},
		{ID: 3, Name: "Pink Gopher", Age: 4},
		{ID: 4, Name: "Grey Gopher", Age: 10},
	}

	for _, user := range users {
		if err := lru.Set(ctx, mem.S(strconv.Itoa(user.ID)), scache.Value(user)); err != nil {
			log.Fatalf("failed to cache user %d: %s", user.ID, err)
		}
	}

	// Fetch user to ensure it is not thrown out when we go over the limit.
	if _, ok := lru.Get(ctx, mem.S(strconv.Itoa(users[1].ID))); !ok {
		log.Fatalf("user with ID %d not found in cache!", users[1].ID)
	}

	newUser := User{ID: 5, Name: "Rainbox Gopher", Age: 1}

	if err := lru.Set(ctx, mem.S(strconv.Itoa(newUser.ID)), scache.Value(newUser)); err != nil {
		log.Fatalf("failed to cache user %d: %s", newUser.ID, err)
	}

	// ... later

	userIDs := getUserIDs(ctx)

	for _, userID := range userIDs {
		entry, ok := lru.Get(ctx, mem.S(strconv.Itoa(userID)))
		if !ok {
			fmt.Printf("user with ID %d not found in cache!\n", userID)
			continue
		}

		fmt.Printf("%+v\n", entry.Value)
	}

	// Output:
	//
	// user with ID 1 not found in cache!
	// {ID:2 Name:Green Gopher Age:7}
	// user with ID 3 not found in cache!
	// {ID:4 Name:Grey Gopher Age:10}
	// {ID:5 Name:Rainbox Gopher Age:1}
}
