package scache_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nussjustin/scache"
)

func testCtx(tb testing.TB) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	tb.Cleanup(cancel)
	return ctx
}

func testCtxWithCancel(tb testing.TB) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	tb.Cleanup(cancel)
	return ctx, cancel
}

func blockingFunc(ctx context.Context) scache.Func[string, string] {
	return func(funcCtx context.Context, _ string) (string, error) {
		select {
		case <-ctx.Done():
			return "", context.Cause(ctx)
		case <-funcCtx.Done():
			return "", context.Cause(funcCtx)
		}
	}
}

func panicFunc(v any) scache.Func[string, string] {
	return func(context.Context, string) (string, error) {
		panic(v)
	}
}

func valueFunc(v string) scache.Func[string, string] {
	return func(context.Context, string) (string, error) {
		return v, nil
	}
}

type errorAdapter struct {
	err error
}

var _ scache.Adapter[string, string] = (*panicAdapter)(nil)

func (e errorAdapter) Get(_ context.Context, _ string) (value string, age time.Duration, err error) {
	return "", 0, e.err
}

func (e errorAdapter) Set(context.Context, string, string) error {
	panic("Set called on errorAdapter")
}

type memoryValue struct {
	age time.Duration
	v   string
}

type memoryAdapter struct {
	mu    sync.Mutex
	m     map[string]memoryValue
	chans []chan<- string
}

var _ scache.Adapter[string, string] = (*memoryAdapter)(nil)

func newMemoryAdapter() *memoryAdapter {
	return &memoryAdapter{m: make(map[string]memoryValue)}
}

func (m *memoryAdapter) Get(_ context.Context, key string) (value string, age time.Duration, err error) {
	m.mu.Lock()
	v, ok := m.m[key]
	m.mu.Unlock()

	if !ok {
		return value, age, scache.ErrNotFound
	}

	return v.v, v.age, nil
}

func (m *memoryAdapter) Set(_ context.Context, key string, value string) error {
	m.setWithAge(key, value, 0)
	return nil
}

func (m *memoryAdapter) setWithAge(key string, value string, age time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.m[key] = memoryValue{v: value, age: age}

	for _, c := range m.chans {
		c <- key
	}
	m.chans = m.chans[:0]
}

func (m *memoryAdapter) changeCh() <-chan string {
	ch := make(chan string, 1)

	m.mu.Lock()
	m.chans = append(m.chans, ch)
	m.mu.Unlock()

	return ch
}

type noopAdapter struct{}

var _ scache.Adapter[string, string] = (*noopAdapter)(nil)

func (n noopAdapter) Get(_ context.Context, _ string) (value string, age time.Duration, err error) {
	return value, age, scache.ErrNotFound
}

func (n noopAdapter) Set(context.Context, string, string) error {
	return nil
}

type panicAdapter struct{}

var _ scache.Adapter[string, string] = (*panicAdapter)(nil)

func (p panicAdapter) Get(_ context.Context, _ string) (value string, age time.Duration, err error) {
	panic("Get called on panicAdapter")
}

func (p panicAdapter) Set(context.Context, string, string) error {
	panic("Set called on panicAdapter")
}

type staticValueAdapter struct{ age time.Duration }

var _ scache.Adapter[string, string] = staticValueAdapter{}

func (s staticValueAdapter) Get(_ context.Context, key string) (value string, age time.Duration, err error) {
	return key, s.age, nil
}

func (s staticValueAdapter) Set(ctx context.Context, key string, value string) error {
	return nil
}
