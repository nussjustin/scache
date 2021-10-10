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

type getter[T any] interface {
	Get(ctx context.Context, key string) (val T, age time.Duration, ok bool)
}

func assertCacheGet[T any](tb testing.TB, c getter[T], ctx context.Context, key string, want T) {
	tb.Helper()

	got, _, ok := c.Get(ctx, key)
	if !ok {
		tb.Fatalf("failed to get key %q", key)
	}

	if !reflect.DeepEqual(want, got) {
		tb.Fatalf("failed to assert value: want %v got %#v", want, got)
	}
}

func assertCacheMiss[T any](tb testing.TB, c getter[T], ctx context.Context, key string) {
	tb.Helper()

	if val, _, ok := c.Get(ctx, key); ok {
		tb.Fatalf("failed to assert cache miss: got value %#v", val)
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

func assertStats[T any](tb testing.TB, c *lookup.Cache[T], want lookup.Stats) {
	tb.Helper()

	var got lookup.Stats

	for i := 0; i < 50; i++ {
		got = c.Stats()

		if reflect.DeepEqual(want, got) {
			return
		}

		// stats are updated in the background, so try a few times
		time.Sleep(10 * time.Millisecond)
	}

	if !reflect.DeepEqual(want, got) {
		tb.Fatalf("failed to assert stats:\n\twant %+v,\n\t got %+v", want, got)
	}
}

func waitForInFlight[T any](tb testing.TB, c *lookup.Cache[T]) {
	for i := 0; i < 50; i++ {
		if c.Running() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if n := c.Running(); n != 0 {
		tb.Fatalf("timeout waiting for in-flight lookups to exit. got %d in-flight lookups", n)
	}
}

type fakeTime time.Duration

func (ft *fakeTime) Add(d time.Duration) {
	atomic.AddInt64((*int64)(ft), int64(d))
}

func (ft *fakeTime) NowFunc() time.Time {
	return time.Unix(0, atomic.LoadInt64((*int64)(ft)))
}

type callbackCache[T any] struct {
	getFunc func(ctx context.Context, key string) (val T, age time.Duration, ok bool)
	setFunc func(ctx context.Context, key string, val T) error
}

func (c callbackCache[T]) Get(ctx context.Context, key string) (val T, age time.Duration, ok bool) {
	return c.getFunc(ctx, key)
}

func (c callbackCache[T]) Set(ctx context.Context, key string, val T) error {
	return c.setFunc(ctx, key, val)
}

type mapCache[T any] map[string]T

func (m mapCache[T]) Get(_ context.Context, key string) (val T, age time.Duration, ok bool) {
	val, ok = m[key]
	return
}

func (m mapCache[T]) Set(_ context.Context, key string, val T) error {
	m[key] = val
	return nil
}

type statsCache[T any] struct {
	c          scache.Cache[T]
	gets, sets uint64
}

func (s *statsCache[T]) Get(ctx context.Context, key string) (val T, age time.Duration, ok bool) {
	atomic.AddUint64(&s.gets, 1)
	return s.c.Get(ctx, key)
}

func (s *statsCache[T]) Set(ctx context.Context, key string, val T) error {
	atomic.AddUint64(&s.sets, 1)
	return s.c.Set(ctx, key, val)
}

type slowCache[T any] struct {
	c        scache.Cache[T]
	getDelay time.Duration
	setDelay time.Duration
}

func (s *slowCache[T]) Get(ctx context.Context, key string) (val T, age time.Duration, ok bool) {
	t := time.NewTimer(s.getDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return val, -1, false
	case <-t.C:
		if s.c != nil {
			return s.c.Get(ctx, key)
		}
		return val, -1, true
	}
}

func (s *slowCache[T]) Set(ctx context.Context, key string, val T) error {
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

func TestLookupCache(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU[string](4)

		lookupFunc := func(ctx context.Context, key string) (val string, err error) {
			if key != "hello" {
				return "", fmt.Errorf("wrong key: %s", key)
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
			c := lookup.NewCache[string](lru, lookupFunc, &lookup.Opts{ErrorHandler: onError})

			assertCacheGet[string](t, c, ctx, "hello", "hello")

			assertCacheMiss[string](t, c, ctx, "Hello")
			assertLookupError("Hello", "wrong key: Hello")

			assertCacheMiss[string](t, c, ctx, "HELLO")
			assertLookupError("HELLO", "wrong key: HELLO")

			assertCacheGet[string](t, c, ctx, "hello", "hello")

			assertLookupError("HELLO", "wrong key: HELLO")
			assertStats(t, c, lookup.Stats{
				Hits:    1,
				Errors:  2,
				Lookups: 3,
				Misses:  3,
			})
		}
	})

	t.Run("Get Canceled", func(t *testing.T) {
		t.Run("Canceled", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			sc := &slowCache[string]{getDelay: 250 * time.Millisecond}
			c := lookup.NewCache[string](sc, func(_ context.Context, _ string) (val string, err error) {
				return "world", nil
			}, &lookup.Opts{Timeout: 150 * time.Millisecond})

			assertCacheMiss[string](t, c, ctx, "hello")
			assertStats(t, c, lookup.Stats{Errors: 1})
		})

		t.Run("Timeout", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()

			sc := &slowCache[string]{getDelay: 250 * time.Millisecond}
			c := lookup.NewCache[string](sc, func(_ context.Context, _ string) (val string, err error) {
				return "world", nil
			}, &lookup.Opts{Timeout: 150 * time.Millisecond})

			assertCacheMiss[string](t, c, ctx, "hello")
			assertStats(t, c, lookup.Stats{Errors: 1})
		})
	})

	t.Run("Lookup Timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU[string](4)

		var lookupDelayMu sync.Mutex
		lookupDelay := 150 * time.Millisecond

		lookupFunc := func(ctx context.Context, key string) (val string, err error) {
			lookupDelayMu.Lock()
			delay := lookupDelay
			lookupDelayMu.Unlock()

			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
			return strings.ToUpper(key), nil
		}

		c := lookup.NewCache[string](lru, lookupFunc, &lookup.Opts{Timeout: 250 * time.Millisecond})

		{
			ctx, cancel := context.WithTimeout(ctx, time.Millisecond)
			defer cancel()
			assertCacheMiss[string](t, c, ctx, "hello1")
		}

		{
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			assertCacheMiss[string](t, c, ctx, "hello2")
		}

		assertCacheGet[string](t, c, ctx, "hello3", "HELLO3")

		lookupDelayMu.Lock()
		lookupDelay = 350 * time.Millisecond
		lookupDelayMu.Unlock()

		assertCacheMiss[string](t, c, ctx, "hello4")
		assertStats(t, c, lookup.Stats{Errors: 1, Lookups: 4, Misses: 4})
	})

	t.Run("ErrSkip", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU[string](4)

		lookupFunc := func(ctx context.Context, key string) (val string, err error) {
			return "world", lookup.ErrSkip
		}

		c := lookup.NewCache[string](lru, lookupFunc, nil)

		assertCacheGet[string](t, c, ctx, "hello", "world")
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1})
		assertCacheMiss[string](t, lru, ctx, "hello")
	})

	t.Run("No Update on Hit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		stats := &statsCache[string]{c: mapCache[string]{"hello": "world"}}

		c := lookup.NewCache[string](stats, func(ctx context.Context, key string) (val string, err error) {
			panic("lookup function called")
		}, nil)

		assertCacheGet[string](t, c, ctx, "hello", "world")
		assertStats(t, c, lookup.Stats{Hits: 1})
	})

	t.Run("Panic", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cc := callbackCache[string]{
			getFunc: func(context.Context, string) (val string, age time.Duration, ok bool) {
				return
			},
			setFunc: func(context.Context, string, string) error {
				panic("PANIC WHILE SET'TING VALUE")
			},
		}

		lookupFunc := func(ctx context.Context, key string) (val string, err error) {
			if key == "Hello" {
				panic("INVALID KEY")
			}
			return strings.ToUpper(key), nil
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

		c := lookup.NewCache[string](cc, lookupFunc, &lookup.Opts{PanicHandler: onPanic})
		assertCacheMiss[string](t, c, ctx, "Hello")
		assertLookupPanic("Hello", "INVALID KEY")
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1, Panics: 1})

		assertCacheGet[string](t, c, ctx, "World", "WORLD")
		waitForInFlight(t, c)
		assertLookupPanic("World", "PANIC WHILE SET'TING VALUE")
		assertStats(t, c, lookup.Stats{Lookups: 2, Misses: 2, Panics: 2})

		c = lookup.NewCache[string](cc, lookupFunc, &lookup.Opts{PanicHandler: nil})
		assertCacheMiss[string](t, c, ctx, "Hello") // check that the panic is still handled
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1, Panics: 1})
	})

	t.Run("Refresh", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var ft fakeTime

		lru := scache.NewLRU[int](4)
		lru.NowFunc = ft.NowFunc

		var lookups uint64

		c := lookup.NewCache[int](
			lru,
			func(_ context.Context, key string) (val int, err error) {
				return int(atomic.AddUint64(&lookups, 1)), nil
			},
			&lookup.Opts{RefreshAfter: 1 * time.Second})

		assertCacheGet[int](t, c, ctx, "hello", 1)
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1, Hits: 0})
		assertCacheGet[int](t, lru, ctx, "hello", 1)

		ft.Add(1 * time.Second)

		assertCacheGet[int](t, c, ctx, "hello", 2)
		assertStats(t, c, lookup.Stats{Lookups: 2, Misses: 1, Hits: 1, Refreshes: 1})
		assertCacheGet[int](t, lru, ctx, "hello", 2)
	})

	t.Run("Refresh Error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var ft fakeTime

		lru := scache.NewLRUWithTTL[int](4, 2*time.Second)
		lru.NowFunc = ft.NowFunc

		stats := &statsCache[int]{c: lru}

		var lookups uint64
		c := lookup.NewCache[int](
			stats,
			func(_ context.Context, key string) (val int, err error) {
				atomic.AddUint64(&lookups, 1)
				return 0, errors.New("some error")
			},
			&lookup.Opts{RefreshAfter: 1 * time.Second})

		if err := lru.Set(ctx, "hello", 1); err != nil {
			t.Fatalf("failed to add value to cache: %s", err)
		}

		assertCacheGet[int](t, c, ctx, "hello", 1)
		waitForInFlight(t, c)

		ft.Add(1 * time.Second)

		assertCacheGet[int](t, c, ctx, "hello", 1)
		waitForInFlight(t, c)

		ft.Add(2 * time.Second)

		assertCacheMiss[int](t, c, ctx, "hello")
		assertStats(t, c, lookup.Stats{
			Errors:    2,
			Hits:      2,
			Lookups:   2,
			Misses:    1,
			Refreshes: 1,
		})
	})

	t.Run("Refresh In Background", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var ft fakeTime

		lru := scache.NewLRU[int](4)
		lru.NowFunc = ft.NowFunc

		var lookups uint64

		c := lookup.NewCache[int](
			lru,
			func(_ context.Context, key string) (val int, err error) {
				return int(atomic.AddUint64(&lookups, 1)), nil
			},
			&lookup.Opts{
				BackgroundRefresh: true,
				RefreshAfter:      1 * time.Second,
			})

		assertCacheGet[int](t, c, ctx, "hello", 1)
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1, Hits: 0})
		assertCacheGet[int](t, lru, ctx, "hello", 1)

		ft.Add(1 * time.Second)

		assertCacheGet[int](t, c, ctx, "hello", 1)
		waitForInFlight(t, c)

		assertCacheGet[int](t, lru, ctx, "hello", 2)
		assertStats(t, c, lookup.Stats{Lookups: 2, Misses: 1, Hits: 1, Refreshes: 1})
		assertCacheGet[int](t, c, ctx, "hello", 2)
	})

	t.Run("Refresh Jitter", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var ft fakeTime

		lru := scache.NewLRU[int](4)
		lru.NowFunc = ft.NowFunc

		var lookups uint64

		c := lookup.NewCache[int](
			lru,
			func(_ context.Context, key string) (val int, err error) {
				return int(atomic.AddUint64(&lookups, 1)), nil
			},
			&lookup.Opts{
				RefreshAfter: 1 * time.Second,
				RefreshAfterJitterFunc: func() time.Duration {
					return 100 * time.Millisecond
				},
			})

		assertCacheGet[int](t, c, ctx, "hello", 1)
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1, Hits: 0})
		assertCacheGet[int](t, lru, ctx, "hello", 1)

		ft.Add(900 * time.Millisecond)

		assertCacheGet[int](t, c, ctx, "hello", 2)
		assertStats(t, c, lookup.Stats{Lookups: 2, Misses: 1, Hits: 1, Refreshes: 1})
		assertCacheGet[int](t, lru, ctx, "hello", 2)
	})

	t.Run("Set Timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lookupFunc := func(_ context.Context, key string) (val string, err error) {
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

		sc := &slowCache[string]{setDelay: 500 * time.Millisecond, c: scache.NewLRU[string](4)}
		c := lookup.NewCache[string](
			sc,
			lookupFunc,
			&lookup.Opts{
				SetErrorHandler: func(key string, err error) {
					lastErrKey, lastErr = key, err
					lastErrC <- struct{}{}
				},
				SetTimeout: time.Millisecond,
			})

		assertCacheGet[string](t, c, ctx, "hello", "HELLO")
		assertSetError("hello", context.DeadlineExceeded.Error())
		assertStats(t, c, lookup.Stats{
			Errors:  1,
			Lookups: 1,
			Misses:  1,
		})
	})

	t.Run("Simple", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU[string](4)

		lookupFunc := func(ctx context.Context, key string) (val string, err error) {
			return strings.ToUpper(key), nil
		}

		c := lookup.NewCache[string](lru, lookupFunc, nil)

		assertCacheGet[string](t, c, ctx, "hello", "HELLO")
		assertCacheGet[string](t, c, ctx, "HeLlO", "HELLO")
		assertCacheGet[string](t, c, ctx, "HELLO", "HELLO")
		assertStats(t, c, lookup.Stats{Lookups: 3, Misses: 3})

		assertCacheGet[string](t, lru, ctx, "hello", "HELLO")
		assertCacheGet[string](t, lru, ctx, "HeLlO", "HELLO")
		assertCacheGet[string](t, lru, ctx, "HELLO", "HELLO")

		assertCacheGet[string](t, c, ctx, "hello", "HELLO")
		assertCacheGet[string](t, c, ctx, "HeLlO", "HELLO")
		assertCacheGet[string](t, c, ctx, "HELLO", "HELLO")
		assertCacheGet[string](t, c, ctx, "hELLo", "HELLO")
		assertStats(t, c, lookup.Stats{Lookups: 4, Hits: 3, Misses: 4})

		assertCacheGet[string](t, lru, ctx, "hELLo", "HELLO")
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

	lookupFunc := func(_ context.Context, key string) (val string, err error) {
		time.Sleep(25 * time.Millisecond)
		callsMu.Lock()
		calls[key]++
		callsMu.Unlock()
		return key, nil
	}

	c := lookup.NewCache[string](scache.NewLRU[string](4), lookupFunc, nil)

	checkKeys := func() {
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			for _, key := range keys {
				wg.Add(1)
				go func(key string) {
					defer wg.Done()
					_, _, _ = c.Get(ctx, key)
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

func ExampleCache() {
	c := lookup.NewCache[string](scache.NewLRU[string](32), func(ctx context.Context, key string) (val string, err error) {
		return strings.ToUpper(key), nil
	}, nil)

	// later...

	val, _, _ := c.Get(context.Background(), "hello")
	if val != "HELLO" {
		panic("something went wrong... PANIC")
	}
}
