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

	"go4.org/mem"

	"github.com/nussjustin/scache"
	"github.com/nussjustin/scache/lookup"
)

type getter[T any] interface {
	Get(ctx context.Context, key mem.RO) (entry scache.EntryView[T], ok bool)
}

func assertCacheGet[T any](tb testing.TB, c getter[T], ctx context.Context, key string, want T) {
	tb.Helper()

	got, ok := c.Get(ctx, mem.S(key))
	if !ok {
		tb.Fatalf("failed to get key %q", key)
	}

	if !reflect.DeepEqual(want, got.Value) {
		tb.Fatalf("failed to assert value: want %v got %#v", want, got.Value)
	}
}

func assertCacheMiss[T any](tb testing.TB, c getter[T], ctx context.Context, key string) {
	tb.Helper()

	if got, ok := c.Get(ctx, mem.S(key)); ok {
		tb.Fatalf("failed to assert cache miss: got value %#v", got.Value)
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

func (ft *fakeTime) Now() time.Time {
	return time.Unix(0, atomic.LoadInt64((*int64)(ft)))
}

type callbackCache[T any] struct {
	getFunc func(ctx context.Context, key mem.RO) (entry scache.EntryView[T], ok bool)
	setFunc func(ctx context.Context, key mem.RO, entry scache.Entry[T]) error
}

var _ scache.Cache[string] = callbackCache[string]{}

func (c callbackCache[T]) Get(ctx context.Context, key mem.RO) (entry scache.EntryView[T], ok bool) {
	entry, ok = c.getFunc(ctx, key)
	entry.Key = key
	return entry, ok
}

func (c callbackCache[T]) Set(ctx context.Context, key mem.RO, entry scache.Entry[T]) error {
	return c.setFunc(ctx, key, entry)
}

type mapCache[T any] map[uint64]scache.EntryView[T]

var _ scache.Cache[string] = mapCache[string]{}

func newMapCache[T any](m map[string]T) mapCache[T] {
	c := mapCache[T]{}
	for k, v := range m {
		_ = c.Set(context.Background(), mem.S(k), scache.Value(v))
	}
	return c
}

func (m mapCache[T]) Get(_ context.Context, key mem.RO) (entry scache.EntryView[T], ok bool) {
	entry, ok = m[key.MapHash()]
	return
}

func (m mapCache[T]) Set(_ context.Context, key mem.RO, entry scache.Entry[T]) error {
	entry.CreatedAt = time.Now()
	entry.Key = key
	m[key.MapHash()] = entry.View()
	return nil
}

type slowCache[T any] struct {
	c        scache.Cache[T]
	getDelay time.Duration
	setDelay time.Duration
}

var _ scache.Cache[string] = &slowCache[string]{}

func (s *slowCache[T]) Get(ctx context.Context, key mem.RO) (entry scache.EntryView[T], ok bool) {
	t := time.NewTimer(s.getDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return scache.EntryView[T]{}, false
	case <-t.C:
		if s.c != nil {
			return s.c.Get(ctx, key)
		}
		return scache.EntryView[T]{}, true
	}
}

func (s *slowCache[T]) Set(ctx context.Context, key mem.RO, entry scache.Entry[T]) error {
	t := time.NewTimer(s.setDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		if s.c != nil {
			return s.c.Set(ctx, key, entry)
		}
		return nil
	}
}

func TestLookupCache(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lru := scache.NewLRU[string](4)

		lookupFunc := func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			if !key.EqualString("hello") {
				return scache.Value(""), fmt.Errorf("wrong key: %s", key.StringCopy())
			}
			return scache.Value(key.StringCopy()), nil
		}

		var lastMu sync.Mutex
		var lastErrKey mem.RO
		var lastErr error
		onError := func(key mem.RO, err error) {
			lastMu.Lock()
			defer lastMu.Unlock()
			lastErrKey, lastErr = key, err
		}

		assertLookupError := func(wantKey, wantErr string) {
			t.Helper()

			lastMu.Lock()
			defer lastMu.Unlock()

			if !lastErrKey.EqualString(wantKey) {
				t.Fatalf("failed to assert key in error handler: want %q, got %q", wantKey, lastErrKey.StringCopy())
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
			c := lookup.NewCache[string](sc, func(_ context.Context, _ mem.RO) (entry scache.Entry[string], err error) {
				return scache.Value("world"), nil
			}, &lookup.Opts{Timeout: 150 * time.Millisecond})

			assertCacheMiss[string](t, c, ctx, "hello")
			assertStats(t, c, lookup.Stats{Errors: 1})
		})

		t.Run("Timeout", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()

			sc := &slowCache[string]{getDelay: 250 * time.Millisecond}
			c := lookup.NewCache[string](sc, func(_ context.Context, _ mem.RO) (entry scache.Entry[string], err error) {
				return scache.Value("world"), nil
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

		lookupFunc := func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			lookupDelayMu.Lock()
			delay := lookupDelay
			lookupDelayMu.Unlock()

			select {
			case <-ctx.Done():
				return scache.Value(""), ctx.Err()
			case <-time.After(delay):
			}
			return scache.Value(strings.ToUpper(key.StringCopy())), nil
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

		lookupFunc := func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			return scache.Value("world"), lookup.ErrSkip
		}

		c := lookup.NewCache[string](lru, lookupFunc, nil)

		assertCacheGet[string](t, c, ctx, "hello", "world")
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1})
		assertCacheMiss[string](t, lru, ctx, "hello")
	})

	t.Run("No Update on Hit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		stats := newMapCache[string](map[string]string{"hello": "world"})

		c := lookup.NewCache[string](stats, func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			panic("lookup function called")
		}, nil)

		assertCacheGet[string](t, c, ctx, "hello", "world")
		assertStats(t, c, lookup.Stats{Hits: 1})
	})

	t.Run("Panic", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cc := callbackCache[string]{
			getFunc: func(context.Context, mem.RO) (entry scache.EntryView[string], ok bool) {
				return
			},
			setFunc: func(context.Context, mem.RO, scache.Entry[string]) error {
				panic("PANIC WHILE SET'TING VALUE")
			},
		}

		lookupFunc := func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			if key.EqualString("Hello") {
				panic("INVALID KEY")
			}
			return scache.Value(strings.ToUpper(key.StringCopy())), nil
		}

		var lastMu sync.Mutex
		var lastPanicKey mem.RO
		var lastPanic error
		onPanic := func(key mem.RO, v any) {
			lastMu.Lock()
			defer lastMu.Unlock()
			lastPanicKey, lastPanic = key, errors.New(v.(string))
		}

		assertLookupPanic := func(wantKey, wantPanic string) {
			t.Helper()

			lastMu.Lock()
			defer lastMu.Unlock()

			if !lastPanicKey.EqualString(wantKey) {
				t.Fatalf("failed to assert key in panic handler: want %q, got %q", wantKey, lastPanicKey.StringCopy())
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

		lru := scache.NewLRU[uint64](4)
		lru.NowFunc = ft.Now

		var lookups uint64

		c := lookup.NewCache[uint64](
			lru,
			func(_ context.Context, key mem.RO) (entry scache.Entry[uint64], err error) {
				entry = scache.Value(atomic.AddUint64(&lookups, 1))
				entry.ExpiresAt = ft.Now().Add(2 * time.Second)
				return
			},
			&lookup.Opts{RefreshBeforeExpiry: 1 * time.Second})
		c.NowFunc = ft.Now

		assertCacheGet[uint64](t, c, ctx, "hello", 1)
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1, Hits: 0})
		assertCacheGet[uint64](t, lru, ctx, "hello", 1)

		ft.Add(1 * time.Second)

		assertCacheGet[uint64](t, c, ctx, "hello", 2)
		assertStats(t, c, lookup.Stats{Lookups: 2, Misses: 1, Hits: 1, Refreshes: 1})
		assertCacheGet[uint64](t, lru, ctx, "hello", 2)
	})

	t.Run("Refresh Error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var ft fakeTime

		lru := scache.NewLRU[int](4)
		lru.NowFunc = ft.Now

		var lookups uint64
		c := lookup.NewCache[int](
			lru,
			func(_ context.Context, key mem.RO) (entry scache.Entry[int], err error) {
				atomic.AddUint64(&lookups, 1)
				return scache.Value(0), errors.New("some error")
			},
			&lookup.Opts{RefreshBeforeExpiry: 1 * time.Second})
		c.NowFunc = ft.Now

		e := scache.Value(1)
		e.ExpiresAt = ft.Now().Add(2 * time.Second)

		if err := lru.Set(ctx, mem.S("hello"), e); err != nil {
			t.Fatalf("failed to add value to cache: %s", err)
		}

		assertCacheGet[int](t, c, ctx, "hello", 1)
		waitForInFlight(t, c)

		assertStats(t, c, lookup.Stats{
			Errors:    0,
			Hits:      1,
			Lookups:   0,
			Misses:    0,
			Refreshes: 0,
		})

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

		lru := scache.NewLRU[uint64](4)
		lru.NowFunc = ft.Now

		var lookups uint64

		c := lookup.NewCache[uint64](
			lru,
			func(_ context.Context, key mem.RO) (entry scache.Entry[uint64], err error) {
				entry = scache.Value(atomic.AddUint64(&lookups, 1))
				entry.ExpiresAt = ft.Now().Add(2 * time.Second)
				return
			},
			&lookup.Opts{
				BackgroundRefresh:   true,
				RefreshBeforeExpiry: 1 * time.Second,
			})
		c.NowFunc = ft.Now

		assertCacheGet[uint64](t, c, ctx, "hello", 1)
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1, Hits: 0})
		assertCacheGet[uint64](t, lru, ctx, "hello", 1)

		ft.Add(1 * time.Second)

		assertCacheGet[uint64](t, c, ctx, "hello", 1)
		waitForInFlight(t, c)

		assertCacheGet[uint64](t, lru, ctx, "hello", 2)
		assertStats(t, c, lookup.Stats{Lookups: 2, Misses: 1, Hits: 1, Refreshes: 1})
		assertCacheGet[uint64](t, c, ctx, "hello", 2)
	})

	t.Run("Refresh Jitter", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var ft fakeTime

		lru := scache.NewLRU[uint64](4)
		lru.NowFunc = ft.Now

		var lookups uint64

		c := lookup.NewCache[uint64](
			lru,
			func(_ context.Context, key mem.RO) (entry scache.Entry[uint64], err error) {
				entry = scache.Value(atomic.AddUint64(&lookups, 1))
				entry.ExpiresAt = ft.Now().Add(2 * time.Second)
				return
			},
			&lookup.Opts{
				RefreshBeforeExpiry: 1 * time.Second,
				RefreshBeforeExpiryJitterFunc: func(key mem.RO) time.Duration {
					if !key.EqualString("hello") {
						panic("unexpected key")
					}
					return 100 * time.Millisecond
				},
			})
		c.NowFunc = ft.Now

		assertCacheGet[uint64](t, c, ctx, "hello", 1)
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1, Hits: 0})

		ft.Add(800 * time.Millisecond)

		assertCacheGet[uint64](t, c, ctx, "hello", 1)
		assertStats(t, c, lookup.Stats{Lookups: 1, Misses: 1, Hits: 1})

		ft.Add(100 * time.Millisecond)

		assertCacheGet[uint64](t, c, ctx, "hello", 2)
		assertStats(t, c, lookup.Stats{Lookups: 2, Misses: 1, Hits: 2, Refreshes: 1})
	})

	t.Run("Set Timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		lookupFunc := func(_ context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			return scache.Value(strings.ToUpper(key.StringCopy())), nil
		}

		var lastErr error
		var lastErrKey mem.RO
		lastErrC := make(chan struct{}, 1)

		assertSetError := func(wantKey, wantErr string) {
			t.Helper()

			select {
			case <-lastErrC:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("timeout waiting for error handler to be called")
			}

			if !lastErrKey.EqualString(wantKey) {
				t.Fatalf("failed to assert key in error handler: want %q, got %q", wantKey, lastErrKey.StringCopy())
			}
			assertError(t, lastErr, wantErr)
		}

		sc := &slowCache[string]{setDelay: 500 * time.Millisecond, c: scache.NewLRU[string](4)}
		c := lookup.NewCache[string](
			sc,
			lookupFunc,
			&lookup.Opts{
				SetErrorHandler: func(key mem.RO, err error) {
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

		lookupFunc := func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			return scache.Value(strings.ToUpper(key.StringCopy())), nil
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

	lookupFunc := func(_ context.Context, key mem.RO) (entry scache.Entry[string], err error) {
		time.Sleep(25 * time.Millisecond)
		callsMu.Lock()
		calls[key.StringCopy()]++
		callsMu.Unlock()
		return scache.Value(key.StringCopy()), nil
	}

	c := lookup.NewCache[string](scache.NewLRU[string](4), lookupFunc, nil)

	checkKeys := func() {
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			for _, key := range keys {
				wg.Add(1)
				go func(key string) {
					defer wg.Done()
					_, _ = c.Get(ctx, mem.S(key))
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
	f := func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
		// Return some value. We simply return the key in UPPER CASE.
		return scache.Value(strings.ToUpper(key.StringCopy())), nil
	}

	c := lookup.NewCache[string](scache.NewLRU[string](32), f, nil)

	// later...

	entry, _ := c.Get(context.Background(), mem.S("hello"))
	if entry.Value != "HELLO" {
		panic("something went wrong... PANIC")
	}
}
