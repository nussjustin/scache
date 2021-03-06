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
	groups []lookupGroup
}

const defaultTimeout = 250 * time.Millisecond

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
		opts.SetTimeout = defaultTimeout
	}

	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}

	l := &Cache{
		opts: *opts,

		c: c,
		f: f,

		hasher: sharding.NewHasher(),
		groups: make([]lookupGroup, 256),
	}
	for i := range l.groups {
		l.groups[i] = lookupGroup{
			cache:   l,
			entries: make(map[uint64]*lookupGroupEntry),
		}
	}
	return l
}

// Get returns the value for the given key.
//
// If the key is not found in the underlying Cache or the cached value is stale (its age is greater
// than the refresh duration, see WithRefreshAfter) it will be looked up using the lookup function
// specified in NewCache.
//
// When the lookup of a stale value fails, Get will continue to return the old value.
func (l *Cache) Get(ctx context.Context, key string) (val interface{}, age time.Duration, ok bool) {
	hash := l.hasher.Hash(key)
	idx := hash & uint64(len(l.groups)-1)
	return l.groups[idx].get(hash, key).wait(ctx)
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
	for i := range l.groups {
		s := &l.groups[i]
		s.mu.Lock()
		n += len(s.entries)
		s.mu.Unlock()
	}
	return n
}

// Stats returns statistics about all completed lookups and cache accesses.
func (l *Cache) Stats() Stats {
	var stats Stats
	for i := range l.groups {
		s := &l.groups[i]
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
	// BackgroundRefresh enables refreshing of expired values in the background and serving of stale values for values
	// which are getting refreshed in the background.
	BackgroundRefresh bool

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

	// RefreshAfterJitterFunc can be used to add jitter to the value specified in RefreshAfter.
	//
	// This can be useful to avoid thundering herd problems when many entries need to be
	// refreshed at the same time.
	//
	// If nil no jitter will be added.
	RefreshAfterJitterFunc func() time.Duration

	// SetErrorHandler will be called when updating the underlying Cache fails.
	//
	// If nil errors will be ignored.
	SetErrorHandler func(key string, err error)

	// SetTimeout specifies the timeout for saving a new or updated value into the underlying Cache.
	//
	// If SetTimeout is <= 0 a default timeout of 250ms will be used.
	SetTimeout time.Duration
}

func (o Opts) shouldRefresh(age time.Duration) bool {
	if o.RefreshAfter <= 0 {
		return false
	}
	limit := o.RefreshAfter
	if o.RefreshAfterJitterFunc != nil {
		limit -= o.RefreshAfterJitterFunc()
	}
	return age >= limit
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

type lookupGroup struct {
	cache *Cache

	mu      sync.Mutex
	entries map[uint64]*lookupGroupEntry
	stats   Stats
}

func (lg *lookupGroup) add(hash uint64, key string) *lookupGroupEntry {
	lge := &lookupGroupEntry{
		cache: lg.cache,

		group: lg,
		hash:  hash,
		key:   key,

		done: make(chan struct{}),
	}
	lg.entries[hash] = lge
	return lge
}

func (lg *lookupGroup) get(hash uint64, key string) *lookupGroupEntry {
	lg.mu.Lock()
	sge, ok := lg.entries[hash]
	if !ok {
		sge = lg.add(hash, key)
	}
	lg.mu.Unlock()

	if !ok {
		go sge.run(context.Background())
	}

	return sge
}

func (lg *lookupGroup) remove(sge *lookupGroupEntry) {
	lg.mu.Lock()
	delete(lg.entries, sge.hash)
	lg.stats = sge.group.stats.add(sge.stats)
	lg.mu.Unlock()
}

type lookupGroupEntry struct {
	cache *Cache
	group *lookupGroup
	hash  uint64
	key   string

	stats Stats
	done  chan struct{}

	val interface{}
	age time.Duration
	ok  bool
}

func (lge *lookupGroupEntry) closeAndRemove() {
	select {
	case <-lge.done:
	default:
		// only happens when we got an error or a panic
		close(lge.done)
	}

	lge.group.remove(lge)
}

func (lge *lookupGroupEntry) handleErr(err error) {
	lge.stats.Errors++
	if lge.cache.opts.ErrorHandler != nil {
		lge.cache.opts.ErrorHandler(lge.key, err)
	}
}

func (lge *lookupGroupEntry) handlePanic(v interface{}) {
	lge.stats.Panics++
	if lge.cache.opts.PanicHandler != nil {
		lge.cache.opts.PanicHandler(lge.key, v)
	}
}

func (lge *lookupGroupEntry) fetchCached(ctx context.Context) error {
	if lge.val, lge.age, lge.ok = lge.cache.c.Get(ctx, lge.key); lge.ok {
		return nil
	}
	return ctx.Err()
}

func (lge *lookupGroupEntry) lookupAndCache(baseCtx, ctx context.Context) {
	lge.stats.Lookups++

	val, err := lge.cache.f(ctx, lge.key)
	if err != nil {
		lge.handleErr(err)
		return
	}

	select {
	case <-lge.done:
		// we are doing a background refresh and have already allowed the waiters to use the cached values.
		// since there may still be concurrent waiters we must not be modifying lge here to avoid a race
	default:
		lge.val, lge.age, lge.ok = val, 0, true

		// close manually so that waiters can return without having to wait for the cache to be updated
		close(lge.done)
	}

	if val != nil || lge.cache.opts.CacheNil {
		lge.cache.set(baseCtx, lge.key, val, &lge.stats)
	}
}

func (lge *lookupGroupEntry) run(baseCtx context.Context) {
	defer func() {
		if v := recover(); v != nil {
			lge.handlePanic(v)
		}

		lge.closeAndRemove()
	}()

	ctx, cancel := context.WithTimeout(baseCtx, lge.cache.opts.Timeout)
	defer cancel()

	if err := lge.fetchCached(ctx); err != nil {
		lge.handleErr(err)
		return
	}

	if !lge.ok {
		lge.stats.Misses++
	} else {
		lge.stats.Hits++

		if !lge.cache.opts.shouldRefresh(lge.age) {
			return
		}

		lge.stats.Refreshes++

		if lge.cache.opts.BackgroundRefresh {
			close(lge.done)
		}
	}

	lge.lookupAndCache(baseCtx, ctx)
}

func (lge *lookupGroupEntry) wait(ctx context.Context) (val interface{}, age time.Duration, ok bool) {
	select {
	case <-ctx.Done():
		return nil, 0, false
	case <-lge.done:
		return lge.val, lge.age, lge.ok
	}
}
