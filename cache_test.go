package scache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nussjustin/scache"
)

func assertCacheContains(
	tb testing.TB,
	a scache.Adapter[string, string],
	key string,
	value string,
) {
	tb.Helper()

	gotValue, _, gotErr := a.Get(tb.Context(), key)

	if gotValue != value {
		tb.Errorf("got cached value %q, want %q", gotValue, value)
	}

	if gotErr != nil {
		tb.Errorf("got error from adapter %q, want not error", gotErr)
	}
}

func assertCacheNotContains(
	tb testing.TB,
	a scache.Adapter[string, string],
	key string,
) {
	tb.Helper()

	gotValue, _, gotErr := a.Get(tb.Context(), key)

	if gotErr == nil {
		tb.Errorf("got cached value %q, wanted miss", gotValue)
		return
	}

	if !errors.Is(gotErr, scache.ErrNotFound) {
		tb.Errorf("got error %v, want %v", gotErr, scache.ErrNotFound)
	}
}

func assertGet(
	ctx context.Context,
	tb testing.TB,
	c *scache.Cache[string, string],
	key string,
	value string,
	err error,
) {
	tb.Helper()

	gotValue, gotErr := c.Get(ctx, key)

	if gotValue != value {
		tb.Errorf("got value %q, expected %q", gotValue, value)
	}

	if (err != nil || gotErr != nil) && !errors.Is(gotErr, err) {
		tb.Errorf("got error %v, expected %v", gotErr, err)
	}
}

func TestLoad(t *testing.T) {
	t.Run("Expired Context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		c := scache.New(panicAdapter{}, panicFunc("unreachable"))

		assertGet(ctx, t, c, "new-key", "", ctx.Err())
	})

	t.Run("Get error", func(t *testing.T) {
		ctx := t.Context()

		err := errors.New("test error")

		c := scache.New(errorAdapter{err}, panicFunc("unreachable"))

		assertGet(ctx, t, c, "new-key", "", err)
	})

	t.Run("Fresh", func(t *testing.T) {
		ctx := t.Context()

		a := newMemoryAdapter()
		a.setWithAge("existing-key", "initial value", 0)

		c := scache.New(a, panicFunc("unreachable"))

		assertGet(ctx, t, c, "existing-key", "initial value", nil)
	})

	t.Run("Miss", func(t *testing.T) {
		t.Run("Loaded", func(t *testing.T) {
			ctx := t.Context()

			a := newMemoryAdapter()

			c := scache.New(a, valueFunc("computed value"))

			assertGet(ctx, t, c, "new-key", "computed value", nil)
			assertCacheContains(t, a, "new-key", "computed value")
		})

		t.Run("Caller Canceled", func(t *testing.T) {
			ctx := t.Context()

			callCtx, callCancel := context.WithCancelCause(ctx)
			defer callCancel(context.Canceled)

			a := newMemoryAdapter()

			c := scache.New(a, func(funcCtx context.Context, key string) (string, error) {
				callCancel(context.Canceled)
				return blockingFunc(ctx)(funcCtx, key)
			})

			assertGet(callCtx, t, c, "new-key", "", context.Canceled)
			assertCacheNotContains(t, a, "new-key")
		})

		t.Run("Load Canceled", func(t *testing.T) {
			ctx := t.Context()

			a := newMemoryAdapter()

			err := errors.New("test error")

			c := scache.New(a, blockingFunc(ctx),
				scache.WithContextFunc(func() (context.Context, context.CancelFunc) {
					subCtx, cancel := context.WithCancelCause(ctx)
					cancel(err)
					return subCtx, func() {
						cancel(context.Canceled)
					}
				}))

			assertGet(ctx, t, c, "new-key", "", err)
			assertCacheNotContains(t, a, "new-key")
		})

		t.Run("Load Panic", func(t *testing.T) {
			ctx := t.Context()

			err := errors.New("test error")

			c := scache.New(noopAdapter{}, panicFunc(err))

			assertGet(ctx, t, c, "new-key", "", err)
		})
	})

	t.Run("Stale", func(t *testing.T) {
		jitterFunc := func() time.Duration { return time.Second }

		t.Run("No refresh", func(t *testing.T) {
			ctx := t.Context()

			a := newMemoryAdapter()
			a.setWithAge("existing-key", "initial value", time.Second)

			c := scache.New(a,
				panicFunc("unreachable"),
				scache.WithJitterFunc(jitterFunc),
				scache.WithStaleAfter(time.Second),
				scache.WithMaxStale(3*time.Second))

			assertGet(ctx, t, c, "existing-key", "initial value", nil)
			assertCacheContains(t, a, "existing-key", "initial value")
		})

		t.Run("Refresh", func(t *testing.T) {
			ctx := t.Context()

			a := newMemoryAdapter()
			a.setWithAge("existing-key", "initial value", time.Second)

			waitC := a.changeCh()

			c := scache.New(a,
				valueFunc("computed value"),
				scache.WithRefreshStale(true),
				scache.WithJitterFunc(jitterFunc),
				scache.WithStaleAfter(time.Second),
				scache.WithMaxStale(3*time.Second))

			assertGet(ctx, t, c, "existing-key", "initial value", nil)

			select {
			case key := <-waitC:
				if key != "existing-key" {
					t.Errorf("got change for wrong key %q, want %q", key, "existing-key")
				}
			case <-time.After(time.Second):
				t.Error("timeout waiting for change")
			}

			assertCacheContains(t, a, "existing-key", "computed value")
		})

		t.Run("Refresh and wait", func(t *testing.T) {
			ctx := t.Context()

			a := newMemoryAdapter()
			a.setWithAge("existing-key", "initial value", time.Second)

			c := scache.New(a,
				valueFunc("computed value"),
				scache.WithRefreshStale(true),
				scache.WithWaitForRefresh(1),
				scache.WithTimerFunc(func(time.Duration) <-chan time.Time {
					ch := make(chan time.Time)

					context.AfterFunc(ctx, func() {
						close(ch)
					})

					return ch
				}),
				scache.WithJitterFunc(jitterFunc),
				scache.WithStaleAfter(time.Second),
				scache.WithMaxStale(3*time.Second))

			assertGet(ctx, t, c, "existing-key", "computed value", nil)
			assertCacheContains(t, a, "existing-key", "computed value")
		})

		t.Run("Refresh and wait with fresh before jitter", func(t *testing.T) {
			ctx := t.Context()

			a := newMemoryAdapter()
			a.setWithAge("existing-key", "initial value", time.Second)

			waitC := a.changeCh()

			c := scache.New(a,
				valueFunc("computed value"),
				scache.WithRefreshStale(true),
				scache.WithWaitForRefresh(1),
				scache.WithTimerFunc(func(time.Duration) <-chan time.Time {
					ch := make(chan time.Time)

					context.AfterFunc(ctx, func() {
						close(ch)
					})

					return ch
				}),
				scache.WithJitterFunc(jitterFunc),
				scache.WithStaleAfter(2*time.Second),
				scache.WithMaxStale(3*time.Second))

			assertGet(ctx, t, c, "existing-key", "initial value", nil)

			select {
			case key := <-waitC:
				if key != "existing-key" {
					t.Errorf("got change for wrong key %q, want %q", key, "existing-key")
				}
			case <-time.After(time.Second):
				t.Error("timeout waiting for change")
			}

			assertCacheContains(t, a, "existing-key", "computed value")
		})

		t.Run("Refresh and wait timeout", func(t *testing.T) {
			ctx := t.Context()

			a := newMemoryAdapter()
			a.setWithAge("existing-key", "initial value", time.Second)

			valueC := make(chan string, 1)
			waitC := a.changeCh()

			c := scache.New(a,
				func(ctx context.Context, _ string) (string, error) {
					select {
					case value := <-valueC:
						return value, nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				},
				scache.WithRefreshStale(true),
				scache.WithWaitForRefresh(1),
				scache.WithTimerFunc(func(time.Duration) <-chan time.Time {
					ch := make(chan time.Time)
					close(ch)
					return ch
				}),
				scache.WithJitterFunc(jitterFunc),
				scache.WithStaleAfter(time.Second),
				scache.WithMaxStale(3*time.Second))

			assertGet(ctx, t, c, "existing-key", "initial value", nil)

			valueC <- "computed value"

			select {
			case key := <-waitC:
				if key != "existing-key" {
					t.Errorf("got change for wrong key %q, want %q", key, "existing-key")
				}
			case <-time.After(time.Second):
				t.Error("timeout waiting for change")
			}

			assertCacheContains(t, a, "existing-key", "computed value")
		})

		// Same as "Refresh" but with no minimum age for staleness.
		t.Run("Refresh immediately", func(t *testing.T) {
			ctx := t.Context()

			a := newMemoryAdapter()
			a.setWithAge("existing-key", "initial value", 0)

			waitC := a.changeCh()

			c := scache.New(a,
				valueFunc("computed value"),
				scache.WithRefreshStale(true),
				scache.WithStaleAfter(0))

			assertGet(ctx, t, c, "existing-key", "initial value", nil)

			select {
			case key := <-waitC:
				if key != "existing-key" {
					t.Errorf("got change for wrong key %q, want %q", key, "existing-key")
				}
			case <-time.After(time.Second):
				t.Error("timeout waiting for change")
			}

			assertCacheContains(t, a, "existing-key", "computed value")
		})
	})

	t.Run("Expired", func(t *testing.T) {
		t.Run("Loaded", func(t *testing.T) {
			ctx := t.Context()

			a := newMemoryAdapter()
			a.setWithAge("existing-key", "initial value", 2*time.Second)

			c := scache.New(a, valueFunc("computed value"),
				scache.WithStaleAfter(time.Second),
				scache.WithMaxStale(time.Second))

			assertGet(ctx, t, c, "new-key", "computed value", nil)
			assertCacheContains(t, a, "new-key", "computed value")
		})

		t.Run("Caller Canceled", func(t *testing.T) {
			ctx := t.Context()

			callCtx, callCancel := context.WithCancel(ctx)
			defer callCancel()

			a := newMemoryAdapter()
			a.setWithAge("existing-key", "initial value", 2*time.Second)

			c := scache.New(a,
				func(funcCtx context.Context, key string) (string, error) {
					callCancel()
					return blockingFunc(ctx)(funcCtx, key)
				},
				scache.WithStaleAfter(time.Second),
				scache.WithMaxStale(time.Second))

			assertGet(callCtx, t, c, "existing-key", "", context.Canceled)
			assertCacheContains(t, a, "existing-key", "initial value")
		})

		t.Run("Load Canceled", func(t *testing.T) {
			ctx := t.Context()

			a := newMemoryAdapter()
			a.setWithAge("existing-key", "initial value", 2*time.Second)

			err := errors.New("test error")

			c := scache.New(a, blockingFunc(ctx),
				scache.WithStaleAfter(time.Second),
				scache.WithMaxStale(time.Second),
				scache.WithContextFunc(func() (context.Context, context.CancelFunc) {
					subCtx, cancel := context.WithCancelCause(ctx)
					cancel(err)
					return subCtx, func() {
						cancel(context.Canceled)
					}
				}))

			assertGet(ctx, t, c, "existing-key", "", err)
			assertCacheContains(t, a, "existing-key", "initial value")
		})
	})
}

var benchmarkDst string

func BenchmarkLoad(b *testing.B) {
	b.Run("Hit", func(b *testing.B) {
		ctx := b.Context()

		c := scache.New(staticValueAdapter{}, panicFunc("unreachable"))

		for b.Loop() {
			benchmarkDst, _ = c.Get(ctx, "benchmark")
		}
	})

	b.Run("Hit with refresh", func(b *testing.B) {
		ctx := b.Context()

		c := scache.New(staticValueAdapter{time.Second}, valueFunc("benchmark"),
			scache.WithStaleAfter(time.Millisecond),
			scache.WithRefreshStale(true))

		for b.Loop() {
			benchmarkDst, _ = c.Get(ctx, "benchmark")
		}
	})

	b.Run("Hit with wait for refresh", func(b *testing.B) {
		ctx := b.Context()

		c := scache.New(staticValueAdapter{time.Second}, valueFunc("benchmark"),
			scache.WithStaleAfter(time.Millisecond),
			scache.WithRefreshStale(true),
			scache.WithWaitForRefresh(time.Hour))

		for b.Loop() {
			benchmarkDst, _ = c.Get(ctx, "benchmark")
		}
	})

	b.Run("Miss", func(b *testing.B) {
		ctx := b.Context()

		c := scache.New(noopAdapter{}, valueFunc("benchmark"))

		for b.Loop() {
			benchmarkDst, _ = c.Get(ctx, "benchmark")
		}
	})
}
