package lookup_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nussjustin/scache"
	"github.com/nussjustin/scache/lookup"
)

type roCache interface {
	Get(ctx context.Context, key string) (val interface{}, ok bool)
}

func assertCacheGet(tb testing.TB, c roCache, ctx context.Context, key string, want interface{}) {
	tb.Helper()

	got, ok := c.Get(ctx, key)
	if !ok {
		tb.Fatalf("failed to get key %q", key)
	}

	if !reflect.DeepEqual(want, got) {
		tb.Fatalf("failed to assert value: want %v got %v", want, got)
	}
}

func assertCacheMiss(tb testing.TB, c roCache, ctx context.Context, key string) {
	tb.Helper()

	if val, ok := c.Get(ctx, key); ok {
		tb.Fatalf("failed to assert cache miss: got value %v", val)
	}
}

func assertCacheSet(tb testing.TB, c scache.Cache, ctx context.Context, key string, val interface{}) {
	tb.Helper()

	if err := c.Set(ctx, key, val); err != nil {
		tb.Fatalf("failed to set key %q to value %q: %s", key, val, err)
	}
}

func assertError(tb testing.TB, err error, want string) {
	tb.Helper()

	if err == nil {
		tb.Fatalf("failed to assert error: want %q, got <nil>", want)
	} else if got := err.Error(); want != got {
		tb.Fatalf("failed to assert error: want %q, got %q", want, got)
	}
}

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

		lookupFunc := func(ctx context.Context, key string) (val interface{}, err error) {
			if key != "hello" {
				return nil, fmt.Errorf("wrong key: %s", key)
			}
			return key, nil
		}

		var lastMu sync.Mutex
		var lastErrKey string
		var lastErr error
		onError := func(key string, err error) {
			lastMu.Lock()
			defer lastMu.Unlock()
			lastErrKey, lastErr = key, err
		}

		assertLookupError := func(wantKey, wantErr string) {
			t.Helper()

			lastMu.Lock()
			defer lastMu.Unlock()

			if wantKey != lastErrKey {
				t.Fatalf("failed to assert key in error handler: want %q, got %q", wantKey, lastErrKey)
			}
			assertError(t, lastErr, wantErr)
		}

		{
			c := lookup.NewCache(lru, lookupFunc, lookup.WithErrorHandler(onError))

			assertCacheGet(t, c, ctx, "hello", "hello")

			assertCacheMiss(t, c, ctx, "Hello")
			assertLookupError("Hello", "wrong key: Hello")

			assertCacheMiss(t, c, ctx, "HELLO")
			assertLookupError("HELLO", "wrong key: HELLO")

			assertCacheGet(t, c, ctx, "hello", "hello")

			assertLookupError("HELLO", "wrong key: HELLO")
		}
	})

	t.Run("Get Timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sc := &slowCache{getDelay: 250 * time.Millisecond}
		c := lookup.NewCache(sc, func(_ context.Context, _ string) (val interface{}, err error) {
			t.Fatal("Func called unexpectedly")
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

		var lookupDelayMu sync.Mutex
		lookupDelay := 150 * time.Millisecond

		lookupFunc := func(ctx context.Context, key string) (val interface{}, err error) {
			lookupDelayMu.Lock()
			delay := lookupDelay
			lookupDelayMu.Unlock()

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			return strings.ToUpper(key), nil
		}

		c := lookup.NewCache(lru, lookupFunc, lookup.WithTimeout(250*time.Millisecond))

		{
			ctx, cancel := context.WithTimeout(ctx, time.Millisecond)
			defer cancel()
			assertCacheMiss(t, c, ctx, "hello1")
		}

		{
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			assertCacheMiss(t, c, ctx, "hello2")
		}

		assertCacheGet(t, c, ctx, "hello3", "HELLO3")

		lookupDelayMu.Lock()
		lookupDelay = 350 * time.Millisecond
		lookupDelayMu.Unlock()

		assertCacheMiss(t, c, ctx, "hello4")
	})

	t.Run("Nil", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU(4)

		lookupFunc := func(ctx context.Context, key string) (val interface{}, err error) {
			return nil, nil
		}

		c := lookup.NewCache(lru, lookupFunc)

		assertCacheGet(t, c, ctx, "hello", nil)
		assertCacheGetWithWait(t, c, ctx, "hello", nil)
	})

	t.Run("Panic", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU(4)

		lookupFunc := func(ctx context.Context, key string) (val interface{}, err error) {
			panic(strings.ToUpper(key))
		}

		var lastMu sync.Mutex
		var lastPanicKey string
		var lastPanic error
		onPanic := func(key string, v interface{}) {
			lastMu.Lock()
			defer lastMu.Unlock()
			lastPanicKey, lastPanic = key, errors.New(v.(string))
		}

		assertLookupPanic := func(wantKey, wantPanic string) {
			t.Helper()

			lastMu.Lock()
			defer lastMu.Unlock()

			if wantKey != lastPanicKey {
				t.Fatalf("failed to assert key in panic handler: want %q, got %q", wantKey, lastPanicKey)
			}
			assertError(t, lastPanic, wantPanic)
		}

		c := lookup.NewCache(lru, lookupFunc, lookup.WithPanicHandler(onPanic))

		assertCacheMiss(t, c, ctx, "Hello")
		assertLookupPanic("Hello", "HELLO")

		c = lookup.NewCache(lru, lookupFunc, lookup.WithPanicHandler(nil))
		assertCacheMiss(t, c, ctx, "Hello") // check that the panic is still handled
	})

	t.Run("Set Timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lookupFunc := func(_ context.Context, key string) (val interface{}, err error) {
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
		c := lookup.NewCache(
			sc,
			lookupFunc,
			lookup.WithSetErrorHandler(func(key string, err error) {
				lastErrKey, lastErr = key, err
				lastErrC <- struct{}{}
			}),
			lookup.WithSetTimeout(time.Millisecond))

		assertCacheGet(t, c, ctx, "hello", "HELLO")
		assertSetError("hello", context.DeadlineExceeded.Error())
	})

	t.Run("Simple", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU(4)

		var lookupCount uint64
		lookupFunc := func(ctx context.Context, key string) (val interface{}, err error) {
			atomic.AddUint64(&lookupCount, 1)
			return strings.ToUpper(key), nil
		}

		c := lookup.NewCache(lru, lookupFunc)

		assertCacheGet(t, c, ctx, "hello", "HELLO")
		assertCacheGet(t, c, ctx, "HeLlO", "HELLO")
		assertCacheGet(t, c, ctx, "HELLO", "HELLO")
		if want, got := uint64(3), atomic.LoadUint64(&lookupCount); want != got {
			t.Fatalf("failed to assert lookup count: want %d, got %d", want, got)
		}

		assertCacheGetWithWait(t, lru, ctx, "hello", "HELLO")
		assertCacheGetWithWait(t, lru, ctx, "HeLlO", "HELLO")
		assertCacheGetWithWait(t, lru, ctx, "HELLO", "HELLO")

		assertCacheGet(t, c, ctx, "hello", "HELLO")
		assertCacheGet(t, c, ctx, "HeLlO", "HELLO")
		assertCacheGet(t, c, ctx, "HELLO", "HELLO")
		assertCacheGet(t, c, ctx, "hELLo", "HELLO")
		if want, got := uint64(4), atomic.LoadUint64(&lookupCount); want != got {
			t.Fatalf("failed to assert lookup count: want %d, got %d", want, got)
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

	lookupFunc := func(_ context.Context, key string) (val interface{}, err error) {
		time.Sleep(25 * time.Millisecond)
		callsMu.Lock()
		calls[key]++
		callsMu.Unlock()
		return key, nil
	}

	c := lookup.NewCache(scache.NewLRU(4), lookupFunc)

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
