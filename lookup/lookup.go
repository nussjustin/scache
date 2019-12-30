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
//
// If val is nil, by default the value will not be cached. See NewCache and WithCacheNil
// for a way to cache nil values.
//
// Note: val is considered to be nil only if (val == nil) returns true.
type Func func(ctx context.Context, key string) (val interface{}, err error)

// Opt is the type for functions that can be used to customize a Cache.
type Opt func(*lookupOpts)

// Stats contains statistics about a Cache.
type Stats struct {
	// Errors is the total number of panics that were recovered since creating the Cache.
	Errors uint64

	// Hits is the total number of cache hits since creating the Cache.
	Hits uint64

	// Lookups is the total number of lookups since creating the Cache.
	//
	// This is the same as Misses + Refreshes.
	Lookups uint64

	// Misses is the total number of cache misses since creating the Cache.
	Misses uint64

	// Panics is the total number of panics that were recovered since creating the Cache.
	Panics uint64

	// Refreshes is the total number of refreshes since creating the Cache.
	Refreshes uint64
}

func (s Stats) add(so Stats) Stats {
	return Stats{
		Errors:    s.Errors + so.Errors,
		Hits:      s.Hits + so.Hits,
		Lookups:   s.Lookups + so.Lookups,
		Misses:    s.Misses + so.Misses,
		Panics:    s.Panics + so.Panics,
		Refreshes: s.Refreshes + so.Refreshes,
	}
}

type lookupOpts struct {
	cacheNil bool

	lookupErrFn   func(key string, err error)
	lookupTimeout time.Duration

	panicFn func(key string, v interface{})

	refreshAfter time.Duration

	setErrFn   func(key string, err error)
	setTimeout time.Duration
}

// WithCacheNil enables caching of nil values. By default nil is not cached.
func WithCacheNil() Opt {
	return func(opts *lookupOpts) {
		opts.cacheNil = true
	}
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
	stats  Stats
}

type sfGroupEntry struct {
	lc    *Cache
	stats Stats
	done  chan struct{}

	val interface{}
	age time.Duration
	ok  bool

	fresh bool
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
// Note: By default nil values will not be cached. If you want to cache nil
// values, you can pass WithCacheNil() to NewCache.
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
		go g.run(context.Background(), s, key)
	}

	return g.wait(ctx)
}

func (l *Cache) set(stats *Stats, key string, val interface{}) {
	defer func() {
		if v := recover(); v != nil {
			stats.Panics++
			if l.panicFn != nil {
				l.panicFn(key, v)
			}
		}
	}()

	setCtx, cancel := context.WithTimeout(context.Background(), l.setTimeout)
	defer cancel()

	if err := l.c.Set(setCtx, key, val); err != nil {
		stats.Errors++
		if l.setErrFn != nil {
			l.setErrFn(key, err)
		}
	}
}

// Running returns the number of running cache accesses and lookups.
func (l *Cache) Running() int {
	var n int
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		n += len(s.groups)
		s.mu.Unlock()
	}
	return n
}

// Stats returns statistics about all completed lookups and cache accesses.
func (l *Cache) Stats() Stats {
	var stats Stats
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		stats = stats.add(s.stats)
		s.mu.Unlock()
	}
	return stats
}

func (ge *sfGroupEntry) fetch(ctx context.Context, key string) (skipLookup bool) {
	val, age, ok := ge.lc.c.Get(ctx, key)

	switch {
	case ok:
		ge.stats.Hits++
		ge.val, ge.age, ge.ok = val, age, ok
		if ge.lc.refreshAfter <= 0 || ge.lc.refreshAfter > ge.age {
			return true
		}
		ge.stats.Refreshes++
	case ctx.Err() != nil:
		ge.handleErr(key, ctx.Err())
		return true
	default:
		ge.stats.Misses++
	}

	return false
}

func (ge *sfGroupEntry) handleErr(key string, err error) {
	ge.stats.Errors++
	if ge.lc.lookupErrFn != nil {
		ge.lc.lookupErrFn(key, err)
	}
}

func (ge *sfGroupEntry) handlePanic(key string, v interface{}) {
	ge.stats.Panics++
	if ge.lc.panicFn != nil {
		ge.lc.panicFn(key, v)
	}
}

func (ge *sfGroupEntry) lookup(ctx context.Context, key string) {
	ge.stats.Lookups++

	if val, err := ge.lc.f(ctx, key); err == nil {
		ge.fresh = true
		ge.val, ge.age, ge.ok = val, 0, true
	} else {
		ge.handleErr(key, err)
	}
}

func (ge *sfGroupEntry) run(ctx context.Context, shard *sfGroup, key string) {
	defer func() {
		if v := recover(); v != nil {
			ge.handlePanic(key, v)
		}

		close(ge.done)

		if ge.fresh && ge.ok && (ge.val != nil || ge.lc.cacheNil) {
			ge.lc.set(&ge.stats, key, ge.val)
		}

		shard.mu.Lock()
		delete(shard.groups, key)
		shard.stats = shard.stats.add(ge.stats)
		shard.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, ge.lc.lookupTimeout)
	defer cancel()

	if !ge.fetch(ctx, key) {
		ge.lookup(ctx, key)
	}
}

func (ge *sfGroupEntry) wait(ctx context.Context) (val interface{}, age time.Duration, ok bool) {
	select {
	case <-ctx.Done():
		return nil, 0, false
	case <-ge.done:
		return ge.val, ge.age, ge.ok
	}
}
