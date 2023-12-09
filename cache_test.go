package scache_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nussjustin/scache"
)

func TestCache_Get(t *testing.T) {
	t.Run("Common", func(t *testing.T) {
		ctx := testContext(t)

		b := newTestBackend[int](t)
		_ = b.Set(ctx, "first", scache.Value(1))
		_ = b.Set(ctx, "second", scache.Value(2, "tag1"))
		_ = b.Set(ctx, "third", scache.Value(3, "tag1", "tag2"))

		c := scache.New[int](b, nil)

		assertContains(t, c, ctx, "first", nil, 1)
		assertContains(t, c, ctx, "second", []string{"tag1"}, 2)
		assertContains(t, c, ctx, "third", []string{"tag1", "tag2"}, 3)

		b.assertAccessCount("first", 1)
		b.assertAccessCount("second", 1)
		b.assertAccessCount("third", 1)

		_ = b.Set(ctx, "third", scache.Value(4, "tag2", "tag3", "tag4"))

		assertContains(t, c, ctx, "first", nil, 1)
		assertContains(t, c, ctx, "second", []string{"tag1"}, 2)
		assertContains(t, c, ctx, "third", []string{"tag2", "tag3", "tag4"}, 4)

		b.assertAccessCount("first", 2)
		b.assertAccessCount("second", 2)
		b.assertAccessCount("third", 1)

		b.setError("second", io.EOF)

		item, err := c.Get(ctx, "second")
		assertError(t, err, io.EOF)
		assertMiss(t, item, "second", nil, 0)
	})

	t.Run("Staleness", func(t *testing.T) {
		ctx := testContext(t)

		b := newTestBackend[int](t)
		_ = b.Set(ctx, "first", scache.Value(1))

		clock := newFakeClock()

		c := scache.New[int](b, &scache.CacheOpts{
			StaleDuration: time.Minute,
			SinceFunc:     clock.since,
		})

		assertContains(t, c, ctx, "first", nil, 1)

		b.setExpiresAt("first", clock.now)
		assertContains(t, c, ctx, "first", nil, 1)

		clock.advance(30 * time.Second)

		assertContains(t, c, ctx, "first", nil, 1)

		clock.advance(30 * time.Second)

		item, err := c.Get(ctx, "first")
		assertNoError(t, err)
		assertMiss(t, item, "first", nil, 0)
	})
}

func TestCache_GetMany(t *testing.T) {
	t.Run("Generic", func(t *testing.T) {
		ctx := testContext(t)

		b := newTestBackend[int](t)

		clock := newFakeClock()

		c := scache.New[int](b, &scache.CacheOpts{
			SinceFunc:     clock.since,
			StaleDuration: time.Minute,
		})

		{
			items, err := c.GetMany(ctx)
			assertNoError(t, err)
			assertLen(t, items, 0)
		}

		_ = b.Set(ctx, "first", scache.Value(1))
		_ = b.Set(ctx, "second", scache.Value(2, "tag1"))
		_ = b.Set(ctx, "third", scache.Value(3, "tag1", "tag2"))

		b.assertContains("first", nil, 1)
		b.assertAccessCount("first", 0)

		{
			items, err := c.GetMany(ctx, "first", "second", "third", "fourth")
			assertNoError(t, err)
			assertLen(t, items, 4)

			b.assertAccessCount("first", 1)
			b.assertAccessCount("second", 1)
			b.assertAccessCount("third", 1)

			assertHit(t, items[0], "first", nil, 1)
			assertHit(t, items[1], "second", []string{"tag1"}, 2)
			assertHit(t, items[2], "third", []string{"tag1", "tag2"}, 3)
			assertMiss(t, items[3], "fourth", nil, 0)
		}

		b.setError("second", io.EOF)

		{
			items, err := c.GetMany(ctx, "first", "second", "third", "fourth")
			assertError(t, err, io.EOF)
			assertLen(t, items, 0)

			b.assertAccessCount("first", 2)
			b.assertAccessCount("second", 1)
			b.assertAccessCount("third", 1)
		}

		b.setExpiresAt("first", clock.now.Add(time.Minute))
		b.setExpiresAt("third", clock.now.Add(2*time.Minute))
		clock.advance(2 * time.Minute)

		{
			items, err := c.GetMany(ctx, "first", "third")
			assertNoError(t, err)
			assertLen(t, items, 2)

			assertMiss(t, items[0], "first", nil, 0)
			assertHit(t, items[1], "third", []string{"tag1", "tag2"}, 3)
		}
	})

	t.Run("Optimized", func(t *testing.T) {
		ctx := testContext(t)

		b := newTestBackendWithGetMany[int](t)

		clock := newFakeClock()

		c := scache.New[int](b, &scache.CacheOpts{
			SinceFunc:     clock.since,
			StaleDuration: time.Minute,
		})

		{
			items, err := c.GetMany(ctx)
			assertNoError(t, err)
			assertLen(t, items, 0)

			b.assertGetManyCount(0)
		}

		_ = b.Set(ctx, "first", scache.Value(1))
		_ = b.Set(ctx, "second", scache.Value(2, "tag1"))
		_ = b.Set(ctx, "third", scache.Value(3, "tag1", "tag2"))

		b.assertContains("first", nil, 1)
		b.assertAccessCount("first", 0)

		{
			items, err := c.GetMany(ctx, "first", "second", "third", "fourth")
			assertNoError(t, err)
			assertLen(t, items, 4)

			b.assertAccessCount("first", 1)
			b.assertAccessCount("second", 1)
			b.assertAccessCount("third", 1)
			b.assertAccessCount("fourth", 1)
			b.assertGetManyCount(1)

			assertHit(t, items[0], "first", nil, 1)
			assertHit(t, items[1], "second", []string{"tag1"}, 2)
			assertHit(t, items[2], "third", []string{"tag1", "tag2"}, 3)
			assertMiss(t, items[3], "fourth", nil, 0)
		}

		b.setError("second", io.EOF)

		{
			items, err := c.GetMany(ctx, "first", "second", "third", "fourth")
			assertError(t, err, io.EOF)
			assertLen(t, items, 0)

			b.assertAccessCount("first", 2)
			b.assertAccessCount("second", 1)
			b.assertAccessCount("third", 1)
			b.assertAccessCount("fourth", 1)
			b.assertGetManyCount(2)
		}

		b.setExpiresAt("first", clock.now)
		clock.advance(time.Hour)

		// Our implementation does not check for staleness, so nothing should change
		{
			items, err := c.GetMany(ctx, "first", "third")
			assertNoError(t, err)
			assertLen(t, items, 2)

			assertHit(t, items[0], "first", nil, 1)
			assertHit(t, items[1], "third", []string{"tag1", "tag2"}, 3)
		}
	})
}

func TestCache_Load(t *testing.T) {
	t.Run("Cached", func(t *testing.T) {
		t.Run("Error", func(t *testing.T) {
			t.Run("Default", func(t *testing.T) {
				ctx := testContext(t)

				b := newTestBackend[int](t)
				b.setError("error", io.EOF)

				c := scache.New[int](b, nil)

				v, err := c.Load(ctx, "error", unreachable[int])

				assertError(t, err, io.EOF)
				assertZero(t, v.Value)
			})

			t.Run("Ignored", func(t *testing.T) {
				ctx := testContext(t)

				b := newTestBackend[int](t)
				b.setError("error", io.EOF)

				c := scache.New[int](b, &scache.CacheOpts{IgnoreGetErrors: true})

				const want = 1234

				item, err := c.Load(ctx, "error", fixed(want))

				assertNoError(t, err)
				assertMiss(t, item, "hit", nil, want)
			})
		})

		t.Run("Hit", func(t *testing.T) {
			ctx := testContext(t)

			const want = 1234

			b := newTestBackend[int](t)
			_ = b.Set(ctx, "hit", scache.Value(want))

			c := scache.New[int](b, nil)

			item, err := c.Load(ctx, "hit", unreachable[int])

			assertNoError(t, err)
			assertHit(t, item, "hit", nil, want)
		})
	})

	t.Run("Loader", func(t *testing.T) {
		t.Run("Error", func(t *testing.T) {
			t.Run("Miss", func(t *testing.T) {
				ctx := testContext(t)

				b := newTestBackend[int](t)

				c := scache.New[int](b, nil)

				item, err := c.Load(ctx, "error", fixedError[int](io.EOF))

				assertError(t, err, io.EOF)
				assertMiss(t, item, "error", nil, 0)
			})

			t.Run("Stale", func(t *testing.T) {
				ctx := testContext(t)

				clock := newFakeClock()

				b := newTestBackend[int](t)
				_ = b.Set(ctx, "stale", scache.Value(1234))
				b.setExpiresAt("stale", clock.now.Add(time.Minute+time.Second))

				clock.advance(2 * time.Minute)

				c := scache.New[int](b, &scache.CacheOpts{StaleDuration: time.Minute, SinceFunc: clock.since})

				item, err := c.Load(ctx, "stale", fixedError[int](io.EOF))

				assertError(t, err, io.EOF)
				assertHit(t, item, "stale", nil, 1234)
			})
		})

		t.Run("Panic", func(t *testing.T) {
			ctx := testContext(t)

			b := newTestBackend[int](t)

			c := scache.New[int](b, nil)

			assertPanic(t, func() {
				_, _ = c.Load(ctx, "error", unreachable[int])
			}, "unreachable")

			item, err := b.Get(ctx, "panic")
			assertNoError(t, err)
			assertMiss(t, item, "panic", nil, 0)
		})

		t.Run("Loaded", func(t *testing.T) {
			ctx := testContext(t)

			b := newTestBackend[int](t)

			var count int

			c := scache.New[int](b, nil)

			// First load
			{
				item, err := c.Load(ctx, "loaded", func(_ context.Context, key string, _ scache.Item[int]) (scache.Item[int], error) {
					assertEquals(t, key, "loaded")
					count++
					return scache.Value(count), nil
				})

				assertNoError(t, err)
				assertEquals(t, item.Value, 1)
				assertEquals(t, count, 1)

				b.assertContains("loaded", nil, 1)
				b.assertAccessCount("loaded", 0)
			}

			// Cached value
			{
				item, err := c.Load(ctx, "loaded", unreachable[int])

				assertNoError(t, err)
				assertEquals(t, item.Value, 1)

				b.assertContains("loaded", nil, 1)
				b.assertAccessCount("loaded", 1)
			}

			b.remove("loaded")

			// Second load
			{
				item, err := c.Load(ctx, "loaded", func(_ context.Context, key string, _ scache.Item[int]) (scache.Item[int], error) {
					assertEquals(t, key, "loaded")
					count++
					return scache.Value(count), nil
				})

				assertNoError(t, err)
				assertEquals(t, item.Value, 2)
				assertEquals(t, count, 2)

				b.assertContains("loaded", nil, 2)
				b.assertAccessCount("loaded", 0)
			}
		})

		t.Run("Stale", func(t *testing.T) {
			ctx := testContext(t)

			clock := newFakeClock()

			b := newTestBackend[int](t)
			_ = b.Set(ctx, "stale", scache.Value(1234))

			expiresAt := clock.now.Add(time.Minute + time.Second)
			b.setExpiresAt("stale", expiresAt)

			clock.advance(2 * time.Minute)

			c := scache.New[int](b, &scache.CacheOpts{StaleDuration: time.Minute, SinceFunc: clock.since})

			item, err := c.Load(ctx, "stale", func(ctx context.Context, key string, old scache.Item[int]) (scache.Item[int], error) {
				assertEquals(t, old.ExpiresAt, expiresAt)
				assertEquals(t, old.Hit, true)
				assertEquals(t, old.Value, 1234)
				return scache.Value(old.Value * 2), nil
			})

			assertNoError(t, err)
			assertMiss(t, item, "stale", nil, 2468)
		})

		t.Run("Tags", func(t *testing.T) {
			ctx := testContext(t)

			b := newTestBackend[int](t)

			c := scache.New[int](b, nil)

			// Simple
			{
				item, err := c.Load(ctx, "tagged", func(ctx context.Context, _ string, _ scache.Item[int]) (scache.Item[int], error) {
					scache.Tag(ctx, "tag2")
					scache.Tags(ctx, "tag3", "tag4")
					return scache.Value(1, "tag1", "tag3"), nil
				})

				assertNoError(t, err)
				assertMiss(t, item, "tagged", []string{"tag1", "tag2", "tag3", "tag4"}, 1)

				b.assertContains("tagged", []string{"tag1", "tag2", "tag3", "tag4"}, 1)
			}

			// Nested
			{
				item, err := c.Load(ctx, "outer", func(ctx context.Context, _ string, _ scache.Item[int]) (scache.Item[int], error) {
					scache.Tag(ctx, "outer2")

					item, err := c.Load(ctx, "inner", func(ctx context.Context, _ string, _ scache.Item[int]) (scache.Item[int], error) {
						scache.Tag(ctx, "inner2")
						scache.Tags(ctx, "inner3", "inner4")
						return scache.Value(1, "inner1", "inner3"), nil
					})

					assertNoError(t, err)

					scache.Tags(ctx, "outer3", "outer4")
					return scache.Value(item.Value+1, "outer1", "outer3"), nil
				})

				assertNoError(t, err)
				assertMiss(
					t,
					item,
					"outer",
					[]string{"inner1", "inner2", "inner3", "inner4", "outer1", "outer2", "outer3", "outer4"},
					2,
				)

				b.assertContains("inner", []string{"inner1", "inner2", "inner3", "inner4"}, 1)
				b.assertContains(
					"outer",
					[]string{"inner1", "inner2", "inner3", "inner4", "outer1", "outer2", "outer3", "outer4"},
					2,
				)
			}
		})
	})
}

func BenchmarkCache_Load(b *testing.B) {
	b.Run("Miss", func(b *testing.B) {
		b.Run("NoTags", func(b *testing.B) {
			ctx := testContext(b)

			c := scache.New[int](noopBackend[int]{}, nil)

			b.ReportAllocs()

			loader := fixed(0)

			for i := 0; i < b.N; i++ {
				_, _ = c.Load(ctx, "miss", loader)
			}
		})

		b.Run("Tags", func(b *testing.B) {
			ctx := testContext(b)

			c := scache.New[int](noopBackend[int]{}, nil)

			b.ReportAllocs()

			loader := func(ctx context.Context, _ string, _ scache.Item[int]) (scache.Item[int], error) {
				scache.Tag(ctx, "tag2")
				scache.Tags(ctx, "tag3", "tag4")
				return scache.Value(0, "tag1", "tag3"), nil
			}

			for i := 0; i < b.N; i++ {
				_, _ = c.Load(ctx, "miss", loader)
			}
		})
	})

	b.Run("Hit", func(b *testing.B) {
		ctx := testContext(b)

		c := scache.New[int](newTestBackend[int](b), nil)
		assertNoError(b, c.Set(ctx, "hit", scache.Value(1)))

		b.ReportAllocs()

		loader := unreachable[int]

		for i := 0; i < b.N; i++ {
			_, _ = c.Load(ctx, "hit", loader)
		}
	})
}

type loadSyncTest[T comparable] struct {
	backend    scache.Backend[T]
	opts       scache.CacheOpts
	beforeWait func(causeFunc context.CancelCauseFunc)
	load       scache.Loader[T]
	key        string
	expected   loadSyncTestResult[T]
}

type loadSyncTestResult[T comparable] struct {
	item      scache.Item[T]
	err       error
	recovered any
}

func assertLoadSyncResult[T comparable](tb testing.TB, got loadSyncTestResult[T], want loadSyncTestResult[T]) {
	tb.Helper()

	assertItem(tb, got.item, "synced", want.item)
	assertEquals(tb, got.err, want.err)
	assertEquals(tb, got.recovered, want.recovered)
}

func (l loadSyncTest[T]) run(tb testing.TB, ctx context.Context) {
	tb.Helper()

	loadCtx, loadCtxCancel := context.WithCancelCause(context.Background())
	defer loadCtxCancel(errCleanup)

	opts := l.opts
	opts.LoadSyncContextFunc = func() (context.Context, context.CancelFunc) {
		return loadCtx, func() { loadCtxCancel(errors.New("finished")) }
	}

	c := scache.New[T](l.backend, &opts)

	key := l.key
	if key == "" {
		key = "synced" // easier to spot during debugging
	}

	setLoadSyncBarrier(c, 2)

	var wg sync.WaitGroup

	loadInBackground := func(
		ctx context.Context,
		key string,
		load scache.Loader[T],
	) <-chan loadSyncTestResult[T] {
		wg.Add(1)

		resC := make(chan loadSyncTestResult[T], 1)

		go func() {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					resC <- loadSyncTestResult[T]{recovered: recovered}
				}
			}()

			item, err := c.LoadSync(ctx, key, load)

			resC <- loadSyncTestResult[T]{item: item, err: err}
		}()

		return resC
	}

	result1C := loadInBackground(ctx, key, l.load)
	result2C := loadInBackground(ctx, key, l.load)

	if l.beforeWait != nil {
		l.beforeWait(loadCtxCancel)
	}

	wg.Wait()

	var result1, result2 loadSyncTestResult[T]

	select {
	case result1 = <-result1C:
	default:
		tb.Error("no result received for first goroutine")
		return
	}

	select {
	case result2 = <-result2C:
	default:
		tb.Error("no result received for second goroutine")
		return
	}

	assertLoadSyncResult(tb, result1, l.expected)
	assertLoadSyncResult(tb, result2, l.expected)
}

func TestCache_LoadSync(t *testing.T) {
	t.Run("Cached", func(t *testing.T) {
		t.Run("Error", func(t *testing.T) {
			t.Run("Default", func(t *testing.T) {
				customErr := errors.New("custom err")

				loadSyncTest[int]{
					backend: newBlockingBackend[int](),
					beforeWait: func(cancel context.CancelCauseFunc) {
						cancel(customErr)
					},
					expected: loadSyncTestResult[int]{err: customErr},
				}.run(t, testContext(t))
			})

			t.Run("Ignored", func(t *testing.T) {
				loadSyncTest[int]{
					opts:    scache.CacheOpts{IgnoreGetErrors: true},
					backend: newBlockingBackend[int](),
					load:    fixed(1),
					beforeWait: func(cancel context.CancelCauseFunc) {
						cancel(nil)
					},
					expected: loadSyncTestResult[int]{item: scache.Value(1)},
				}.run(t, testContext(t))
			})
		})

		t.Run("Hit", func(t *testing.T) {
			b := newTestBackend[int](t)
			_ = b.Set(nil, "synced", scache.Value(1234))

			loadSyncTest[int]{
				backend:  b,
				expected: loadSyncTestResult[int]{item: hit(1234)},
			}.run(t, testContext(t))
		})
	})

	t.Run("Loader", func(t *testing.T) {
		t.Run("Error", func(t *testing.T) {
			t.Run("Miss", func(t *testing.T) {
				customErr := errors.New("custom err")

				loadSyncTest[int]{
					backend:  noopBackend[int]{},
					expected: loadSyncTestResult[int]{err: customErr},
					load:     fixedError[int](customErr),
				}.run(t, testContext(t))
			})

			t.Run("Stale", func(t *testing.T) {
				ctx := testContext(t)

				clock := newFakeClock()

				b := newTestBackend[int](t)
				_ = b.Set(ctx, "stale", scache.Value(1234))

				expiresAt := clock.now.Add(time.Minute + time.Second)
				b.setExpiresAt("stale", expiresAt)

				clock.advance(2 * time.Minute)

				customErr := errors.New("custom err")

				loadSyncTest[int]{
					opts:     scache.CacheOpts{StaleDuration: 3 * time.Hour},
					backend:  b,
					expected: loadSyncTestResult[int]{err: customErr},
					key:      "stale",
					load:     fixedError[int](customErr),
				}.run(t, ctx)
			})
		})

		t.Run("Panic", func(t *testing.T) {
			customErr := errors.New("custom err")

			loadSyncTest[int]{
				backend:  noopBackend[int]{},
				expected: loadSyncTestResult[int]{recovered: customErr},
				load:     fixedPanic[int](customErr),
			}.run(t, testContext(t))
		})

		t.Run("Loaded", func(t *testing.T) {
			var counter atomic.Int64

			load := func(context.Context, string, scache.Item[int]) (scache.Item[int], error) {
				n := counter.Add(1)
				return scache.Value(int(n)), nil
			}

			ctx := testContext(t)

			b := newTestBackend[int](t)

			loadSyncTest[int]{
				backend:  b,
				expected: loadSyncTestResult[int]{item: scache.Value(1)},
				load:     load,
			}.run(t, ctx)
			b.assertAccessCount("synced", 0)
			assertEquals(t, counter.Load(), 1)

			loadSyncTest[int]{
				backend:  b,
				expected: loadSyncTestResult[int]{item: hit(1)},
				load:     load,
			}.run(t, ctx)
			b.assertAccessCount("synced", 1)
			assertEquals(t, counter.Load(), 1)

			b.remove("synced")

			loadSyncTest[int]{
				backend:  b,
				expected: loadSyncTestResult[int]{item: scache.Value(2)},
				load:     load,
			}.run(t, ctx)
			b.assertAccessCount("synced", 0)
			assertEquals(t, counter.Load(), 2)
		})

		t.Run("Stale", func(t *testing.T) {
			ctx := testContext(t)

			clock := newFakeClock()

			b := newTestBackend[int](t)
			_ = b.Set(ctx, "stale", scache.Value(1234))

			expiresAt := clock.now.Add(time.Minute + time.Second)
			b.setExpiresAt("stale", expiresAt)

			clock.advance(2 * time.Minute)

			load := func(ctx context.Context, key string, old scache.Item[int]) (scache.Item[int], error) {
				assertEquals(t, old.ExpiresAt, expiresAt)
				assertEquals(t, old.Hit, true)
				assertEquals(t, old.Value, 1234)
				return scache.Value(old.Value * 2), nil
			}

			loadSyncTest[int]{
				backend:  b,
				expected: loadSyncTestResult[int]{item: scache.Value(2468)},
				key:      "stale",
				load:     load,
			}.run(t, ctx)
		})

		t.Run("Tags", func(t *testing.T) {
			ctx := testContext(t)

			b := newTestBackend[int](t)

			load := func(ctx context.Context, _ string, _ scache.Item[int]) (scache.Item[int], error) {
				loadSyncTest[int]{
					backend: b,
					key:     "outer",
					expected: loadSyncTestResult[int]{
						item: scache.Value(2,
							"inner1", "inner2", "inner3", "inner4",
							"outer1", "outer2", "outer3", "outer4",
						),
					},
					load: func(ctx context.Context, _ string, _ scache.Item[int]) (scache.Item[int], error) {
						scache.Tag(ctx, "outer2")
						scache.Tags(ctx, "outer3", "outer4")

						loadSyncTest[int]{
							backend: b,
							key:     "inner",
							expected: loadSyncTestResult[int]{
								item: scache.Value(2, "inner1", "inner2", "inner3", "inner4"),
							},
							load: func(ctx context.Context, _ string, item scache.Item[int]) (scache.Item[int], error) {
								scache.Tag(ctx, "inner2")
								scache.Tags(ctx, "inner3", "inner4")

								return scache.Value(2, "inner1", "inner4"), nil
							},
						}.run(t, ctx)

						return scache.Value(2, "outer1", "outer4"), nil
					},
				}.run(t, ctx)

				return scache.Value(0, "manual"), nil
			}

			item, err := scache.New[int](b, nil).Load(ctx, "no-key", load)

			assertNoError(t, err)
			assertItem(t, item, "no-key", scache.Item[int]{
				Tags: []string{"inner1", "inner2", "inner3", "inner4", "manual", "outer1", "outer2", "outer3", "outer4"},
			})
		})
	})
}

// TODO: Benchmark LoadSync

func TestCache_Set(t *testing.T) {
	ctx := testContext(t)

	b := newTestBackend[int](t)

	thirdTags := []string{"tag1", "tag2"}

	c := scache.New[int](b, nil)

	_ = c.Set(ctx, "first", scache.Value(1))
	_ = c.Set(ctx, "second", scache.Value(2, "tag1"))
	_ = c.Set(ctx, "third", scache.Value(3, thirdTags...))

	thirdTags[1] = "tag0"

	b.assertContains("first", nil, 1)
	b.assertContains("second", []string{"tag1"}, 2)
	b.assertContains("third", []string{"tag1", "tag2"}, 3)
}
