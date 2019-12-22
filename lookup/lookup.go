package lookup

import (
	"context"
	"sync"
	"time"

	"github.com/nussjustin/scache"
	"github.com/nussjustin/scache/internal/sharding"
)

// Cache implements a cache wrapper where on each cache miss the value will be looked
// up via a cache-level lookup function and cached in the underlying cache for further calls.
//
// Cache explicitly does not implement the Cache interface.
type Cache struct {
	lookupOpts

	c scache.Cache
	f Func

	hasher sharding.Hasher
	shards []sfGroup
}

// Func defines a function for looking up values that will be placed in a Cache.
type Func func(ctx context.Context, key string) (val interface{}, err error)

// Opt is the type for functions that can be used to customize a Cache.
type Opt func(*lookupOpts)

type lookupOpts struct {
	lookupErrFn   func(key string, err error)
	lookupTimeout time.Duration

	panicFn func(key string, v interface{})

	refreshAfter time.Duration

	setErrFn   func(key string, err error)
	setTimeout time.Duration
}

// WithErrorHandler can be used to set a handler function that is called when a Lookup
// returns an error.
//
// If nil (the default) errors will be ignored.
func WithErrorHandler(f func(key string, err error)) Opt {
	return func(opts *lookupOpts) {
		opts.lookupErrFn = f
	}
}

// WithPanicHandler can be used to set a handler function that is called when a Lookup
// panics.
//
// If nil (the default) panics will be ignored.
func WithPanicHandler(f func(key string, v interface{})) Opt {
	return func(opts *lookupOpts) {
		opts.panicFn = f
	}
}

// WithRefreshAfter specifies the duration after which values in the cache will be treated
// as missing and refreshed.
//
// If a refresh fails the Cache will keep serving the old value and retry the refresh the
// next time the underlying cache is checked.
//
// If d is <= 0, values will never be treated as missing based on their age.
func WithRefreshAfter(d time.Duration) Opt {
	return func(opts *lookupOpts) {
		opts.refreshAfter = d
	}
}

// WithSetErrorHandler can be used to set a handler function that is called when
// setting a looked up value in the underlying Cache returns an error.
//
// If nil (the default) errors will be ignored.
func WithSetErrorHandler(f func(key string, err error)) Opt {
	return func(opts *lookupOpts) {
		opts.setErrFn = f
	}
}

// WithSetTimeout sets the timeout for setting values in the underlying Cache.
func WithSetTimeout(d time.Duration) Opt {
	return func(opts *lookupOpts) {
		opts.setTimeout = d
	}
}

// WithTimeout sets the timeout for looking up fresh values.
func WithTimeout(d time.Duration) Opt {
	return func(opts *lookupOpts) {
		opts.lookupTimeout = d
	}
}

type sfGroup struct {
	mu     sync.Mutex
	groups map[string]*sfGroupEntry
}

type sfGroupEntry struct {
	lc   *Cache
	done chan struct{}

	val      interface{}
	age      time.Duration
	miss, ok bool
}

// NewCache returns a new *Cache using c as the underlying Cache and f for
// looking up missing values.
//
// The underlying Cache must be safe for concurrent use.
//
// The defaults options when not overridden are:
//
//     WithSetTimeout(250 * time.Millisecond)
//     WithTimeout(250 * time.Millisecond)
//
func NewCache(c scache.Cache, f Func, opts ...Opt) *Cache {
	lopts := lookupOpts{
		lookupTimeout: 250 * time.Millisecond,
		setTimeout:    250 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(&lopts)
	}

	ss := make([]sfGroup, 128) // TODO(nussjustin): Make configurable?
	for i := range ss {
		ss[i].groups = make(map[string]*sfGroupEntry)
	}

	return &Cache{
		lookupOpts: lopts,

		c: c,
		f: f,

		hasher: sharding.NewHasher(),
		shards: ss,
	}
}

// Get returns the value for the given key.
//
// If the key is not found in the underlying Cache or the cached value is stale (its age is greater
// than the refresh duration, see WithRefreshAfter) it will be looked up using the lookup function
// specified in NewCache.
//
// When the lookup of a stale value fails, Get will continue to return the old value.
func (l *Cache) Get(ctx context.Context, key string) (val interface{}, age time.Duration, ok bool) {
	idx := int(l.hasher.Hash(key) % uint64(len(l.shards)))

	s := &l.shards[idx]
	s.mu.Lock()
	g, ok := s.groups[key]
	if !ok {
		g = &sfGroupEntry{lc: l, done: make(chan struct{})}
		s.groups[key] = g
	}
	s.mu.Unlock()

	if !ok {
		go g.lookup(s, key)
	}

	return g.wait(ctx)
}

func (l *Cache) set(key string, val interface{}) {
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

func (l *sfGroupEntry) lookup(shard *sfGroup, key string) {
	defer func() {
		if v := recover(); v != nil {
			if l.lc.panicFn != nil {
				l.lc.panicFn(key, v)
			}
		}

		close(l.done)

		if l.miss && l.ok {
			l.lc.set(key, l.val)
		}

		shard.mu.Lock()
		delete(shard.groups, key)
		shard.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), l.lc.lookupTimeout)
	defer cancel()

	if val, age, ok := l.lc.c.Get(ctx, key); ok {
		l.val, l.age, l.ok = val, age, ok
		if l.lc.refreshAfter <= 0 || l.lc.refreshAfter > l.age {
			return
		}
	} else if ctx.Err() != nil {
		if l.lc.lookupErrFn != nil {
			l.lc.lookupErrFn(key, ctx.Err())
		}
		return
	}

	l.miss = true

	if val, err := l.lc.f(ctx, key); err == nil {
		l.val, l.age, l.ok = val, 0, true
	} else if l.lc.lookupErrFn != nil {
		l.lc.lookupErrFn(key, err)
	}
}

func (l *sfGroupEntry) wait(ctx context.Context) (val interface{}, age time.Duration, ok bool) {
	select {
	case <-ctx.Done():
		return nil, 0, false
	case <-l.done:
		return l.val, l.age, l.ok
	}
}
