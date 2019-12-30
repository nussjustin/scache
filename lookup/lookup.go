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
	opts Opts

	c scache.Cache
	f Func

	hasher sharding.Hasher
	shards []sfGroup
}

// NewCache returns a new *Cache using c as the underlying Cache and f for
// looking up missing values.
//
// The underlying Cache must be safe for concurrent use.
//
// An optional Opts value can be given to control the behaviour of the Cache.
// Setting opts to nil is equivalent to passing a pointer to a zero Opts value.
//
// Any change to opts which after NewCache returns will be ignored.
//
// Note: By default nil values will not be cached. Caching of nil values can
// be enabled by passing custom opts and setting CacheNil to true.
func NewCache(c scache.Cache, f Func, opts *Opts) *Cache {
	if opts == nil {
		opts = &Opts{}
	}

	if opts.SetTimeout <= 0 {
		opts.SetTimeout = 250 * time.Millisecond
	}

	if opts.Timeout <= 0 {
		opts.Timeout = 250 * time.Millisecond
	}

	ss := make([]sfGroup, 128) // TODO(nussjustin): Make configurable?
	for i := range ss {
		ss[i].groups = make(map[string]*sfGroupEntry)
	}

	return &Cache{
		opts: *opts,

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

func (l *Cache) set(ctx context.Context, key string, val interface{}, stats *Stats) {
	defer func() {
		if v := recover(); v != nil {
			stats.Panics++
			if l.opts.PanicHandler != nil {
				l.opts.PanicHandler(key, v)
			}
		}
	}()

	setCtx, cancel := context.WithTimeout(ctx, l.opts.SetTimeout)
	defer cancel()

	if err := l.c.Set(setCtx, key, val); err != nil {
		stats.Errors++
		if l.opts.SetErrorHandler != nil {
			l.opts.SetErrorHandler(key, err)
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

// Func defines a function for looking up values that will be placed in a Cache.
//
// If val is nil, by default the value will not be cached. See NewCache and WithCacheNil
// for a way to cache nil values.
//
// Note: val is considered to be nil only if (val == nil) returns true.
type Func func(ctx context.Context, key string) (val interface{}, err error)

// Opts can be used to configure or change the behaviour of a Cache.
type Opts struct {
	// CacheNil enables caching of nil values when set to true.
	//
	// By default nil values will not be cached.
	//
	// Only untyped nil values will be ignored. Typed nil values will always be cached.
	CacheNil bool

	// ErrorHandler will be called when either the cache access or the lookup returns an error.
	//
	// If nil errors will be ignored.
	ErrorHandler func(key string, err error)

	// Timeout specifies the timeout for the initial cache access + lookup, ignoring the time
	// needed to put a value into the cache.
	//
	// If Timeout is <= 0 a default timeout of 250ms will be used.
	Timeout time.Duration

	// PanicHandler will be called whenever a panic occurs either when accessing the underlying
	// Cache or when looking up a value.
	//
	// If nil panics will be ignored.
	PanicHandler func(key string, v interface{})

	// RefreshAfter specifies the duration after which values in the cache will be treated
	// as missing and refreshed.
	//
	// If a refresh fails the Cache will keep serving the old value and retry the refresh the
	// next time the underlying cache is checked.
	//
	// If RefreshAfter is <= 0, values will never be treated as missing based on their age.
	RefreshAfter time.Duration

	// SetErrorHandler will be called when updating the underlying Cache fails.
	//
	// If nil errors will be ignored.
	SetErrorHandler func(key string, err error)

	// SetTimeout specifies the timeout for saving a new or updated value into the underlying Cache.
	//
	// If SetTimeout is <= 0 a default timeout of 250ms will be used.
	SetTimeout time.Duration
}

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

func (ge *sfGroupEntry) fetch(ctx context.Context, key string) (skipLookup bool) {
	val, age, ok := ge.lc.c.Get(ctx, key)

	switch {
	case ok:
		ge.stats.Hits++
		ge.val, ge.age, ge.ok = val, age, ok
		if ge.lc.opts.RefreshAfter <= 0 || ge.lc.opts.RefreshAfter > ge.age {
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
	if ge.lc.opts.ErrorHandler != nil {
		ge.lc.opts.ErrorHandler(key, err)
	}
}

func (ge *sfGroupEntry) handlePanic(key string, v interface{}) {
	ge.stats.Panics++
	if ge.lc.opts.PanicHandler != nil {
		ge.lc.opts.PanicHandler(key, v)
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
	defer func(ctx context.Context) {
		if v := recover(); v != nil {
			ge.handlePanic(key, v)
		}

		close(ge.done)

		if ge.fresh && ge.ok && (ge.val != nil || ge.lc.opts.CacheNil) {
			ge.lc.set(ctx, key, ge.val, &ge.stats)
		}

		shard.mu.Lock()
		delete(shard.groups, key)
		shard.stats = shard.stats.add(ge.stats)
		shard.mu.Unlock()
	}(ctx)

	ctx, cancel := context.WithTimeout(ctx, ge.lc.opts.Timeout)
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
