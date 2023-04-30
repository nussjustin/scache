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

	c := scache.Noop[string]{}

	assertCacheMiss[string](t, c, ctx, key)
	assertCacheSet[string](t, c, ctx, key, key)
	assertCacheMiss[string](t, c, ctx, key)
}
