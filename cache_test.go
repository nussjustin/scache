package scache_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go4.org/mem"

	"github.com/nussjustin/scache"
)

func assertCacheGet[T any](tb testing.TB, c scache.Cache[T], ctx context.Context, key string, want T) {
	tb.Helper()

	got, ok := c.Get(ctx, mem.S(key))
	if !ok {
		tb.Fatalf("failed to get key %q", key)
	}

	if !reflect.DeepEqual(want, got.Value) {
		tb.Fatalf("failed to assert value: want %v got %v", want, got)
	}
}

func assertCacheGetWithCreatedAt[T any](tb testing.TB, c scache.Cache[T], ctx context.Context, key string, want T, wantCreatedAt time.Time) {
	tb.Helper()

	got, ok := c.Get(ctx, mem.S(key))
	if !ok {
		tb.Fatalf("failed to get key %q", key)
	}

	if !reflect.DeepEqual(want, got.Value) {
		tb.Fatalf("failed to assert value: want %v got %v", want, got)
	}

	if !wantCreatedAt.Equal(got.CreatedAt) {
		tb.Fatalf("failed to assert gotAge: want at least %s got %s", wantCreatedAt, got.CreatedAt)
	}
}

func assertCacheMiss[T any](tb testing.TB, c scache.Cache[T], ctx context.Context, key string) {
	tb.Helper()

	if got, ok := c.Get(ctx, mem.S(key)); ok {
		tb.Fatalf("failed to assert cache miss: got %v", got)
	}
}

func assertCacheSet[T any](tb testing.TB, c scache.Cache[T], ctx context.Context, key string, val T) {
	tb.Helper()

	assertCacheSetEntry(tb, c, ctx, key, scache.Value(val))
}

func assertCacheSetEntry[T any](tb testing.TB, c scache.Cache[T], ctx context.Context, key string, entry scache.Entry[T]) {
	tb.Helper()

	if err := c.Set(ctx, mem.S(key), entry); err != nil {
		tb.Fatalf("failed to set key %q to value %v: %s", key, entry.Value, err)
	}
}

func assertCacheSetError[T any](tb testing.TB, c scache.Cache[T], ctx context.Context, key string, val T, wantErr error) {
	tb.Helper()

	if err := c.Set(ctx, mem.S(key), scache.Value(val)); err == nil {
		tb.Fatalf("failed to assert error for key %q: got value %v", key, val)
	} else if !errors.Is(err, wantErr) {
		tb.Fatalf("failed to assert error for key %q: want %q got %q", key, wantErr, err)
	}
}

type kvPair[T any] struct {
	key   string
	value T
}

func assertValues[T comparable](tb testing.TB, views []scache.EntryView[T], expected []*kvPair[T]) {
	tb.Helper()

	if got, want := len(views), len(expected); got != len(expected) {
		tb.Errorf("got %d views, but expected %d", got, want)
	}

	for i, pair := range expected {
		got := views[i]

		switch {
		case pair == nil && got.CreatedAt.IsZero():
			// match
		case pair == nil:
			tb.Errorf("got unexpected value %v for key %q", got.Value, got.Key.StringCopy())
		case got.Key.EqualString(pair.key) && got.Value == pair.value:
			// match
		case got.Key.EqualString(pair.key):
			tb.Errorf("got value %v for key %q, expected %v", got.Value, pair.key, pair.value)
		default:
			tb.Errorf("got key %q at index %d, expected %q", got.Key.StringCopy(), i, pair.key)
		}
	}
}

func assertNoError(tb testing.TB, err error) {
	tb.Helper()

	if err != nil {
		tb.Fatalf("failed to assert no error: got %v", err)
	}
}

type fakeTime time.Duration

func (ft *fakeTime) Add(d time.Duration) {
	atomic.AddInt64((*int64)(ft), int64(d))
}

func (ft *fakeTime) Now() time.Time {
	return time.Unix(0, atomic.LoadInt64((*int64)(ft)))
}

type fakeCache struct {
	getCalls int
}

func (f *fakeCache) Get(_ context.Context, key mem.RO) (entry scache.EntryView[int], ok bool) {
	f.getCalls++
	return scache.Entry[int]{CreatedAt: time.Now(), Key: key}.View(), true
}

func (f *fakeCache) Set(_ context.Context, _ mem.RO, _ scache.Entry[int]) error {
	return nil
}

type fakeCacheWithGetMany struct {
	fakeCache
	getManyCalls int
}

func (f *fakeCacheWithGetMany) GetMany(_ context.Context, keys ...mem.RO) []scache.EntryView[int] {
	f.getManyCalls++
	s := make([]scache.EntryView[int], len(keys))
	for i, key := range keys {
		s[i] = scache.Entry[int]{CreatedAt: time.Now(), Key: key, Value: i}.View()
	}
	return s
}

func TestGetMany(t *testing.T) {
	t.Run("NoKeys", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c := &fakeCacheWithGetMany{}

		views := scache.GetMany[int](ctx, c)

		if c.getCalls != 0 {
			t.Errorf("got %d calls to Get, expected 0", c.getCalls)
		}

		if c.getManyCalls != 0 {
			t.Errorf("got %d calls to GetMany, expected 0", c.getManyCalls)
		}

		if views != nil {
			t.Errorf("got non-nil result: %v", views)
		}
	})

	t.Run("Fallback", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c := &fakeCache{}

		views := scache.GetMany[int](ctx, c, mem.S("key1"), mem.S("key2"), mem.S("key3"))

		if c.getCalls != 3 {
			t.Errorf("got %d calls to Get, expected 3", c.getCalls)
		}

		assertValues(t, views, []*kvPair[int]{
			{key: "key1", value: 0},
			{key: "key2", value: 0},
			{key: "key3", value: 0},
		})
	})

	t.Run("Interface", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c := &fakeCacheWithGetMany{}

		views := scache.GetMany[int](ctx, c, mem.S("key1"), mem.S("key2"), mem.S("key3"))

		if c.getCalls != 0 {
			t.Errorf("got %d calls to Get, expected 0", c.getCalls)
		}

		if c.getManyCalls != 1 {
			t.Errorf("got %d calls to GetMany, expected 1", c.getManyCalls)
		}

		assertValues(t, views, []*kvPair[int]{
			{key: "key1", value: 0},
			{key: "key2", value: 1},
			{key: "key3", value: 2},
		})
	})
}

func TestLockedCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const keysN = 64

	c := scache.NewLRU[string](keysN)

	var wg sync.WaitGroup
	for i := 0; i < keysN; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			assertCacheSet[string](t, c, ctx, strconv.Itoa(i), strconv.Itoa(i))
		}(i)
	}
	wg.Wait()

	if n := c.Len(); keysN != n {
		t.Fatalf("wanted %d entries in cache, got %d", keysN, n)
	}

	for i := 0; i < keysN; i++ {
		assertCacheGet[string](t, c, ctx, strconv.Itoa(i), strconv.Itoa(i))
	}
}

func TestNoopCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const key = "foo"

	c := scache.NewNoopCache[string]()

	assertCacheMiss[string](t, c, ctx, key)
	assertCacheSet[string](t, c, ctx, key, key)
	assertCacheMiss[string](t, c, ctx, key)
}
