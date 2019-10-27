package scache

import (
	"context"
	"sync"
	"time"
)

// LookupCache implements a cache wrapper where on each cache miss the value will be looked
// up via a cache-global lookup function and cached in the underlying cache for further calls.
//
// LookupCache explicitly does not implement the Cache interface.
type LookupCache struct {
	lookupOpts

	c Cache
	f LookupFunc

	hasher hasher
	shards []sfGroup
}

// LookupFunc defines a function for looking up values that will be placed in a LookupCache.
type LookupFunc func(ctx context.Context, key string) (val interface{}, err error)

// LookupOpt is the type for functions that can be used to customize a LookupCache.
type LookupOpt func(*lookupOpts)

type lookupOpts struct {
	errFn      func(key string, err error)
	panicFn    func(key string, v interface{})
	setErrFn   func(key string, err error)
	setTimeout time.Duration
}

// WithLookupErrorHandler can be used to set a handler function that is called when a Lookup
// returns an error.
//
// If nil (the default) errors will be ignored.
func WithLookupErrorHandler(f func(key string, err error)) LookupOpt {
	return func(opts *lookupOpts) {
		opts.errFn = f
	}
}

// WithLookupPanicHandler can be used to set a handler function that is called when a Lookup
// panics.
//
// If nil (the default) panics will be ignored.
func WithLookupPanicHandler(f func(key string, v interface{})) LookupOpt {
	return func(opts *lookupOpts) {
		opts.panicFn = f
	}
}

// WithLookupSetErrorHandler can be used to set a handler function that is called when
// setting a looked up value in the underlying Cache returns an error.
//
// If nil (the default) errors will be ignored.
func WithLookupSetErrorHandler(f func(key string, err error)) LookupOpt {
	return func(opts *lookupOpts) {
		opts.setErrFn = f
	}
}

// WithLookupSetTimeout sets the timeout for setting values in the underlying Cache.
//
// The default value is 100ms.
func WithLookupSetTimeout(d time.Duration) LookupOpt {
	return func(opts *lookupOpts) {
		opts.setTimeout = d
	}
}

type sfGroup struct {
	mu     sync.Mutex
	groups map[string]*sfGroupEntry
}

type sfGroupEntry struct {
	lc   *LookupCache
	done chan struct{}

	val interface{}
	ok  bool
}

// NewLookupCache returns a new *LookupCache using c as the underlying Cache and f for
// looking up missing values.
//
// The underlying Cache must be safe for concurrent use.
func NewLookupCache(c Cache, f LookupFunc, opts ...LookupOpt) *LookupCache {
	lopts := lookupOpts{
		setTimeout: 250 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(&lopts)
	}

	ss := make([]sfGroup, 64) // TODO(nussjustin): Make configurable?
	for i := range ss {
		ss[i].groups = make(map[string]*sfGroupEntry)
	}

	return &LookupCache{
		lookupOpts: lopts,

		c: c,
		f: f,

		hasher: newHasher(),
		shards: ss,
	}
}

// Get returns the value for the given key.
//
// If the key is not found in the underlying Cache it will be looked up using the lookup
// function specified in NewLookupCache.
func (l *LookupCache) Get(ctx context.Context, key string) (val interface{}, ok bool) {
	idx := int(l.hasher.hash(key) % uint64(len(l.shards)))

	s := &l.shards[idx]
	s.mu.Lock()
	g, ok := s.groups[key]
	if !ok {
		g = &sfGroupEntry{lc: l, done: make(chan struct{})}
		s.groups[key] = g
	}
	s.mu.Unlock()

	if !ok {
		g.lookup(ctx, s, key)
	}

	return g.wait(ctx)
}

func (l *LookupCache) set(key string, val interface{}) {
	defer func() {
		if v := recover(); v != nil && l.panicFn != nil {
			l.panicFn(key, v)
		}
	}()

	setCtx, cancel := context.WithTimeout(context.Background(), l.setTimeout)
	defer cancel()

	if err := l.c.Set(setCtx, key, val); err != nil && l.setErrFn != nil {
		l.setErrFn(key, err)
	}
}

func (l *sfGroupEntry) lookup(ctx context.Context, shard *sfGroup, key string) {
	defer func() {
		if v := recover(); v != nil {
			l.val, l.ok = nil, false

			if l.lc.panicFn != nil {
				l.lc.panicFn(key, v)
			}
		}

		close(l.done)

		if l.ok {
			go l.lc.set(key, l.val)
		}

		shard.mu.Lock()
		delete(shard.groups, key)
		shard.mu.Unlock()
	}()

	if val, ok := l.lc.c.Get(ctx, key); ok {
		l.val, l.ok = val, true
		return
	} else if ctx.Err() != nil {
		if l.lc.errFn != nil {
			l.lc.errFn(key, ctx.Err())
		}
		return
	}

	if val, err := l.lc.f(ctx, key); err == nil {
		l.val, l.ok = val, true
	} else if l.lc.errFn != nil {
		l.lc.errFn(key, err)
	}
}

func (l *sfGroupEntry) wait(ctx context.Context) (val interface{}, ok bool) {
	select {
	case <-ctx.Done():
		return nil, false
	case <-l.done:
		return l.val, l.ok
	}
}
