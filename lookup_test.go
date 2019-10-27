package scache_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nussjustin/scache"
)

type slowCache struct {
	c        scache.Cache
	getDelay time.Duration
	setDelay time.Duration
}

func (s *slowCache) Get(ctx context.Context, key string) (val interface{}, ok bool) {
	t := time.NewTimer(s.getDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil, false
	case <-t.C:
		if s.c != nil {
			return s.c.Get(ctx, key)
		}
		return nil, true
	}
}

func (s *slowCache) Set(ctx context.Context, key string, val interface{}) error {
	t := time.NewTimer(s.setDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		if s.c != nil {
			return s.c.Set(ctx, key, val)
		}
		return nil
	}
}

func assertCacheGetWithWait(tb testing.TB, c roCache, ctx context.Context, key string, want interface{}) {
	tb.Helper()

	var got interface{}
	var ok bool

	for i := 1; i <= 4; i++ {
		if got, ok = c.Get(ctx, key); !ok {
			time.Sleep(time.Duration(i) * 10 * time.Millisecond)
		}
	}

	if !ok {
		tb.Fatalf("failed to get key %q", key)
	}

	if !reflect.DeepEqual(want, got) {
		tb.Fatalf("failed to assert value: want %v got %v", want, got)
	}
}

func TestLookupCache(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU(4)

		lookup := func(ctx context.Context, key string) (val interface{}, err error) {
			if key != "hello" {
				return nil, fmt.Errorf("wrong key: %s", key)
			}
			return key, nil
		}

		var lastErrKey string
		var lastErr error
		onError := func(key string, err error) {
			lastErrKey, lastErr = key, err
		}

		assertLookupError := func(wantKey, wantErr string) {
			t.Helper()

			if wantKey != lastErrKey {
				t.Fatalf("failed to assert key in error handler: want %q, got %q", wantKey, lastErrKey)
			}
			assertError(t, lastErr, wantErr)
		}

		{
			c := scache.NewLookupCache(lru, lookup, scache.WithLookupErrorHandler(onError))

			assertCacheGet(t, c, ctx, "hello", "hello")

			assertCacheMiss(t, c, ctx, "Hello")
			assertLookupError("Hello", "wrong key: Hello")

			assertCacheMiss(t, c, ctx, "HELLO")
			assertLookupError("HELLO", "wrong key: HELLO")

			assertCacheGet(t, c, ctx, "hello", "hello")

			assertLookupError("HELLO", "wrong key: HELLO")
		}

		{
			ctx, cancel := context.WithTimeout(ctx, time.Millisecond)
			defer cancel()

			c := scache.NewLookupCache(&slowCache{getDelay: 250 * time.Millisecond}, lookup, scache.WithLookupErrorHandler(onError))

			assertCacheMiss(t, c, ctx, "Hello")
			assertLookupError("Hello", context.DeadlineExceeded.Error())
		}
	})

	t.Run("Get Timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sc := &slowCache{getDelay: 250 * time.Millisecond}
		c := scache.NewLookupCache(sc, func(_ context.Context, _ string) (val interface{}, err error) {
			t.Fatal("LookupFunc called unexpectedly")
			return nil, nil
		})

		{
			ctx, cancel := context.WithTimeout(ctx, time.Millisecond)
			defer cancel()

			assertCacheMiss(t, c, ctx, "hello")
		}

		{
			ctx, cancel := context.WithCancel(ctx)
			cancel()

			assertCacheMiss(t, c, ctx, "hello")
		}
	})

	t.Run("Lookup Timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU(4)

		lookup := func(ctx context.Context, key string) (val interface{}, err error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
			return strings.ToUpper(key), nil
		}

		c := scache.NewLookupCache(lru, lookup)

		{
			ctx, cancel := context.WithTimeout(ctx, time.Millisecond)
			defer cancel()
			assertCacheMiss(t, c, ctx, "hello")
		}

		{
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			assertCacheMiss(t, c, ctx, "hello")
		}

		assertCacheGet(t, c, ctx, "hello", "HELLO")
	})

	t.Run("Nil", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU(4)

		lookup := func(ctx context.Context, key string) (val interface{}, err error) {
			return nil, nil
		}

		c := scache.NewLookupCache(lru, lookup)

		assertCacheGet(t, c, ctx, "hello", nil)
		assertCacheGetWithWait(t, c, ctx, "hello", nil)
	})

	t.Run("Panic", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU(4)

		lookup := func(ctx context.Context, key string) (val interface{}, err error) {
			panic(strings.ToUpper(key))
		}

		var lastPanicKey string
		var lastPanic error
		onPanic := func(key string, v interface{}) {
			lastPanicKey, lastPanic = key, errors.New(v.(string))
		}

		assertLookupPanic := func(wantKey, wantPanic string) {
			t.Helper()

			if wantKey != lastPanicKey {
				t.Fatalf("failed to assert key in panic handler: want %q, got %q", wantKey, lastPanicKey)
			}
			assertError(t, lastPanic, wantPanic)
		}

		c := scache.NewLookupCache(lru, lookup, scache.WithLookupPanicHandler(onPanic))

		assertCacheMiss(t, c, ctx, "Hello")
		assertLookupPanic("Hello", "HELLO")

		c = scache.NewLookupCache(lru, lookup, scache.WithLookupPanicHandler(nil))
		assertCacheMiss(t, c, ctx, "Hello") // check that the panic is still handled
	})

	t.Run("Set Timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lookup := func(_ context.Context, key string) (val interface{}, err error) {
			return strings.ToUpper(key), nil
		}

		var lastErr error
		var lastErrKey string
		lastErrC := make(chan struct{}, 1)

		assertSetError := func(wantKey, wantErr string) {
			t.Helper()

			select {
			case <-lastErrC:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("timeout waiting for error handler to be called")
			}

			if wantKey != lastErrKey {
				t.Fatalf("failed to assert key in error handler: want %q, got %q", wantKey, lastErrKey)
			}
			assertError(t, lastErr, wantErr)
		}

		sc := &slowCache{setDelay: 500 * time.Millisecond, c: scache.NewLRU(4)}
		c := scache.NewLookupCache(
			sc,
			lookup,
			scache.WithLookupSetErrorHandler(func(key string, err error) {
				lastErrKey, lastErr = key, err
				lastErrC <- struct{}{}
			}),
			scache.WithLookupSetTimeout(time.Millisecond))

		assertCacheGet(t, c, ctx, "hello", "HELLO")
		assertSetError("hello", context.DeadlineExceeded.Error())
	})

	t.Run("Simple", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU(4)

		var lookupCount int
		lookup := func(ctx context.Context, key string) (val interface{}, err error) {
			lookupCount++
			return strings.ToUpper(key), nil
		}

		c := scache.NewLookupCache(lru, lookup)

		assertCacheGet(t, c, ctx, "hello", "HELLO")
		assertCacheGet(t, c, ctx, "HeLlO", "HELLO")
		assertCacheGet(t, c, ctx, "HELLO", "HELLO")
		if 3 != lookupCount {
			t.Fatalf("failed to assert lookup count: want %d, got %d", 3, lookupCount)
		}

		assertCacheGetWithWait(t, lru, ctx, "hello", "HELLO")
		assertCacheGetWithWait(t, lru, ctx, "HeLlO", "HELLO")
		assertCacheGetWithWait(t, lru, ctx, "HELLO", "HELLO")

		assertCacheGet(t, c, ctx, "hello", "HELLO")
		assertCacheGet(t, c, ctx, "HeLlO", "HELLO")
		assertCacheGet(t, c, ctx, "HELLO", "HELLO")
		assertCacheGet(t, c, ctx, "hELLo", "HELLO")
		if 4 != lookupCount {
			t.Fatalf("failed to assert lookup count: want %d, got %d", 3, lookupCount)
		}

		assertCacheGetWithWait(t, lru, ctx, "hELLo", "HELLO")
	})
}

func TestLookupCacheConcurrency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var callsMu sync.Mutex
	calls := make(map[string]int)

	keys := [...]string{
		"key1",
		"key2",
		"key3",
		"key4",
	}

	lookup := func(_ context.Context, key string) (val interface{}, err error) {
		time.Sleep(25 * time.Millisecond)
		callsMu.Lock()
		calls[key]++
		callsMu.Unlock()
		return key, nil
	}

	c := scache.NewLookupCache(scache.NewLRU(4), lookup)

	checkKeys := func() {
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			for _, key := range keys {
				wg.Add(1)
				go func(key string) {
					defer wg.Done()
					_, _ = c.Get(ctx, key)
				}(key)
			}
		}
		wg.Wait()
	}

	assertCalls := func(i int) {
		callsMu.Lock()
		defer callsMu.Unlock()

		for _, key := range keys {
			if want, got := i, calls[key]; want != got {
				t.Errorf("failed to assert call count for key %q: want %d, got %d", key, want, got)
			}
		}
	}

	checkKeys()
	assertCalls(1)

	checkKeys()
	assertCalls(1)
}
