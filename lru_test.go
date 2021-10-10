package scache_test

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"strconv"
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

	if err := lru.Set(ctx, strconv.Itoa(user.ID), user); err != nil {
		log.Fatalf("failed to cache user %d: %s", user.ID, err)
	}

	// ... later

	userID := getUserID(ctx)

	// Ignore age of cache entry
	user, _, ok := lru.Get(ctx, strconv.Itoa(userID))
	if !ok {
		log.Fatalf("user with ID %d not found in cache", userID)
	}

	fmt.Printf("%+v\n", user)

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
		if err := lru.Set(ctx, strconv.Itoa(user.ID), user); err != nil {
			log.Fatalf("failed to cache user %d: %s", user.ID, err)
		}
	}

	// Fetch user to ensure it is not thrown out when we go over the limit.
	if _, _, ok := lru.Get(ctx, strconv.Itoa(users[1].ID)); !ok {
		log.Fatalf("user with ID %d not found in cache!", users[1].ID)
	}

	newUser := User{ID: 5, Name: "Rainbox Gopher", Age: 1}

	if err := lru.Set(ctx, strconv.Itoa(newUser.ID), newUser); err != nil {
		log.Fatalf("failed to cache user %d: %s", newUser.ID, err)
	}

	// ... later

	userIDs := getUserIDs(ctx)

	for _, userID := range userIDs {
		user, _, ok := lru.Get(ctx, strconv.Itoa(userID))
		if !ok {
			fmt.Printf("user with ID %d not found in cache!\n", userID)
			continue
		}

		fmt.Printf("%+v\n", user)
	}

	// Output:
	//
	// user with ID 1 not found in cache!
	// {ID:2 Name:Green Gopher Age:7}
	// user with ID 3 not found in cache!
	// {ID:4 Name:Grey Gopher Age:10}
	// {ID:5 Name:Rainbox Gopher Age:1}
}

func ExampleLRU_withTTL() {
	ctx := context.TODO()

	lru := scache.NewLRUWithTTL[int](32, 100*time.Millisecond)

	if err := lru.Set(ctx, "user:1:friends", 1337); err != nil {
		log.Fatalf("failed to cache users friends: %s", err)
	}

	friends, _, ok := lru.Get(ctx, "user:1:friends")
	if !ok {
		fmt.Println("could not find total number of users friends in cache  :-(")
		return
	}

	fmt.Printf("user 1 got %d friends\n", friends)

	time.Sleep(100 * time.Millisecond) // ... later

	friends, _, ok = lru.Get(ctx, "user:1:friends")
	if !ok {
		fmt.Println("could not find total number of users friends in cache :-(")
		return
	}

	fmt.Printf("user 1 got %d friends\n", friends)

	// Output:
	//
	// user 1 got 1337 friends
	// could not find total number of users friends in cache :-(
}
