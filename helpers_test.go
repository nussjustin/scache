package scache_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"slices"

	"github.com/nussjustin/scache"
	"github.com/nussjustin/scache/internal/testutil"
)

func testContext(tb testing.TB) context.Context {
	ctx, _ := testContextWithCancel(tb)
	return ctx
}

var errCleanup = errors.New("tb.Cleanup called")

func testContextWithCancel(tb testing.TB) (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(context.Background())
	tb.Cleanup(func() {
		cancel(errCleanup)
	})
	return ctx, cancel
}

func hit[T any](v T) scache.Item[T] {
	return scache.Item[T]{Value: v, Hit: true}
}

func init() {
	testutil.LoadSync = loadSyncBarrier
}

var (
	barriersMu sync.Mutex
	barriers   map[any]*barrier
)

type barrier struct {
	mu       sync.Mutex
	cond     sync.Cond
	expected int
	waiting  int
}

func loadSyncBarrier(v any) {
	barriersMu.Lock()
	b := barriers[v]
	barriersMu.Unlock()

	if b == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.waiting++

	if b.waiting == b.expected {
		b.cond.Broadcast()
		return
	}

	b.cond.Wait()
}

func setLoadSyncBarrier(v any, expected int) {
	b := &barrier{expected: expected}
	b.cond.L = &b.mu

	barriersMu.Lock()
	defer barriersMu.Unlock()

	if barriers == nil {
		barriers = make(map[any]*barrier)
	}

	barriers[v] = b
}

type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2006, time.January, 1, 15, 4, 5, 0, time.UTC)}
}

func (f *fakeClock) advance(d time.Duration) {
	f.now = f.now.Add(d)
}

func (f *fakeClock) since(t time.Time) time.Duration {
	return f.now.Sub(t)
}

type blockingBackend[T any] struct{}

func newBlockingBackend[T any]() *blockingBackend[T] {
	return &blockingBackend[T]{}
}

func (e *blockingBackend[T]) Get(ctx context.Context, _ string) (scache.Item[T], error) {
	<-ctx.Done()
	return scache.Item[T]{}, context.Cause(ctx)
}

func (e *blockingBackend[T]) Set(ctx context.Context, _ string, _ scache.Item[T]) error {
	<-ctx.Done()
	return context.Cause(ctx)
}

type noopBackend[T any] struct{}

func (e noopBackend[T]) Get(context.Context, string) (scache.Item[T], error) {
	return scache.Item[T]{}, nil
}

func (e noopBackend[T]) Set(context.Context, string, scache.Item[T]) error {
	return nil
}

type testBackend[T comparable] struct {
	tb testing.TB

	mu sync.Mutex
	m  map[string]*testItem[T]
}

type testItem[T comparable] struct {
	access int
	err    error
	item   scache.Item[T]
}

func newTestBackend[T comparable](tb testing.TB) *testBackend[T] {
	return &testBackend[T]{tb: tb}
}

func (c *testBackend[T]) Get(_ context.Context, key string) (scache.Item[T], error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.m == nil {
		c.m = make(map[string]*testItem[T])
	}

	v := c.m[key]
	if v == nil {
		v = &testItem[T]{}
		c.m[key] = v
	}

	v.access++

	return v.item, v.err
}

func (c *testBackend[T]) Set(_ context.Context, key string, item scache.Item[T]) error {
	item.Hit = true
	item.Tags = slices.Clone(item.Tags)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.m == nil {
		c.m = make(map[string]*testItem[T])
	}

	v := c.m[key]
	if v == nil {
		v = &testItem[T]{}
		c.m[key] = v
	}

	v.access = 0
	v.item = item

	return nil
}

func (c *testBackend[T]) remove(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

func (c *testBackend[T]) setExpiresAt(key string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	v := c.m[key]
	if v == nil || !v.item.Hit {
		c.tb.Fatalf("item with key %q not set", key)
	}

	v.item.ExpiresAt = at
}

func (c *testBackend[T]) setError(key string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.m == nil {
		c.m = make(map[string]*testItem[T])
	}

	v := c.m[key]
	if v == nil {
		v = &testItem[T]{}
		c.m[key] = v
	}

	v.access = 0
	v.err = err
	v.item = scache.Item[T]{}
}

func (c *testBackend[T]) assertAccessCount(key string, n int) {
	c.tb.Helper()

	c.mu.Lock()
	got := c.m[key].access
	c.mu.Unlock()

	if got != n {
		c.tb.Errorf("key %q: got %d access, expected %d", key, got, n)
	}
}

func (c *testBackend[T]) assertContains(key string, tags []string, value T) {
	c.tb.Helper()

	c.mu.Lock()
	defer c.mu.Unlock()

	item := c.m[key]

	if item == nil {
		c.tb.Errorf("item not found for key %q", key)
		return
	}

	assertHit(c.tb, item.item, key, tags, value)
}

type testBackendWithGetMany[T comparable] struct {
	*testBackend[T]
	manyCalls int
}

func newTestBackendWithGetMany[T comparable](tb testing.TB) *testBackendWithGetMany[T] {
	return &testBackendWithGetMany[T]{
		testBackend: newTestBackend[T](tb),
	}
}

func (c *testBackendWithGetMany[T]) GetMany(ctx context.Context, keys ...string) ([]scache.Item[T], error) {
	c.mu.Lock()
	c.manyCalls++
	c.mu.Unlock()

	s := make([]scache.Item[T], len(keys))

	for i, key := range keys {
		item, err := c.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		s[i] = item
	}

	return s, nil
}

func (c *testBackendWithGetMany[T]) assertGetManyCount(n int) {
	c.tb.Helper()

	c.mu.Lock()
	got := c.manyCalls
	c.mu.Unlock()

	if got != n {
		c.tb.Errorf("got %d calls to GetMany, expected %d", got, n)
	}
}

func assertContains[T comparable](tb testing.TB, c scache.Backend[T], ctx context.Context, key string, tags []string, value T) {
	tb.Helper()

	item, err := c.Get(ctx, key)
	assertNoError(tb, err)
	assertHit(tb, item, key, tags, value)
}

func assertNotContains[T comparable](tb testing.TB, c scache.Backend[T], ctx context.Context, key string) {
	tb.Helper()

	var zero T

	item, err := c.Get(ctx, key)
	assertNoError(tb, err)
	assertMiss(tb, item, key, nil, zero)
}

func assertItem[T comparable](tb testing.TB, got scache.Item[T], key string, want scache.Item[T]) {
	tb.Helper()

	switch {
	case got.Hit && !want.Hit:
		tb.Errorf("got hit, expected miss for key %q", key)
		return
	case !got.Hit && want.Hit:
		tb.Errorf("got miss, expected hit for key %q", key)
		return
	}

	sort.Strings(got.Tags)
	sort.Strings(want.Tags)

	if !slices.Equal(got.Tags, want.Tags) {
		tb.Errorf("got tags %v, expected %v for key %q", got.Tags, want.Tags, key)
	}

	if got.Value != want.Value {
		tb.Errorf("got value %v, expected %v for key %q", got.Value, want.Value, key)
	}
}

func assertHit[T comparable](tb testing.TB, item scache.Item[T], key string, tags []string, value T) {
	tb.Helper()
	want := scache.Value[T](value, tags...)
	want.Hit = true
	assertItem(tb, item, key, want)
}

func assertMiss[T comparable](tb testing.TB, item scache.Item[T], key string, tags []string, value T) {
	tb.Helper()
	want := scache.Value[T](value, tags...)
	want.Hit = false
	assertItem(tb, item, key, want)
}

func assertZero[T comparable](tb testing.TB, got T) {
	tb.Helper()

	var zero T
	if got != zero {
		tb.Errorf("got non-zero value %#v", got)
	}
}

func assertEquals[T comparable](tb testing.TB, got T, want T) {
	tb.Helper()

	if got != want {
		tb.Errorf("got %v, want %v", got, want)
	}
}

func assertError(tb testing.TB, got, expected error) {
	tb.Helper()

	if !errors.Is(got, expected) {
		tb.Fatalf("got error %v, expected %v", got, expected)
	}
}

func assertNoError(tb testing.TB, err error) {
	tb.Helper()

	if err != nil {
		tb.Fatalf("unexpected error: %s", err)
	}
}

func assertLen[S ~[]T, T any](tb testing.TB, s S, n int) {
	tb.Helper()

	if len(s) != n {
		tb.Errorf("slice has %d elements, %d expected", len(s), n)
	}
}

func assertPanic[T comparable](tb testing.TB, f func(), want T) {
	defer func() {
		tb.Helper()

		recovered := recover()
		if recovered == nil {
			tb.Errorf("no panic caught")
		}

		switch got := recovered.(type) {
		case T:
			if got != want {
				tb.Errorf("recovered value %v from panic, wanted %v", got, want)
			}
		default:
			tb.Errorf("unexpected recovered value %+v", recovered)
		}
	}()

	f()
}

func fixed[T any](v T) func(context.Context, string, scache.Item[T]) (scache.Item[T], error) {
	return func(context.Context, string, scache.Item[T]) (scache.Item[T], error) {
		return scache.Value(v), nil
	}
}

func fixedError[T any](err error) func(context.Context, string, scache.Item[T]) (scache.Item[T], error) {
	return func(context.Context, string, scache.Item[T]) (scache.Item[T], error) {
		return scache.Item[T]{}, err
	}
}

func fixedPanic[T any](v any) func(context.Context, string, scache.Item[T]) (scache.Item[T], error) {
	return func(context.Context, string, scache.Item[T]) (scache.Item[T], error) {
		panic(v)
	}
}

func unreachable[T any](context.Context, string, scache.Item[T]) (scache.Item[int], error) {
	panic("unreachable")
}
