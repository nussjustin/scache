package lookup

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nussjustin/scache"
	"go4.org/mem"
)

// Cache implements a cache wrapper where on each cache miss the value will be looked
// up via a cache-level lookup function and cached in the underlying cache for further calls.
//
// Cache explicitly does not implement the Cache interface.
type Cache[T any] struct {
	opts Opts

	c scache.Cache[T]
	f Func[T]

	groups []lookupGroup[T]
}

const defaultTimeout = 250 * time.Millisecond

// NewCache returns a new Cache using c for storage and f for looking up
// missing values.
//
// The underlying Cache must be safe for concurrent use.
//
// An optional Opts value can be given to control the behaviour of the Cache.
// Setting opts to nil is equivalent to passing a pointer to a zero Opts value.
//
// Any change to opts which after NewCache returns will be ignored.
func NewCache[T any](c scache.Cache[T], f Func[T], opts *Opts) *Cache[T] {
	if opts == nil {
		opts = &Opts{}
	}

	if opts.SetTimeout <= 0 {
		opts.SetTimeout = defaultTimeout
	}

	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}

	const groups = 64

	cache := &Cache[T]{
		opts: *opts,

		c: c,
		f: f,

		groups: make([]lookupGroup[T], groups),
	}

	for i := range cache.groups {
		cache.groups[i] = lookupGroup[T]{
			cache:   cache,
			entries: make(map[uint64]*lookupGroupEntry[T]),
		}
	}

	return cache
}

// Get returns the value for the given key.
//
// If the key is not found in the underlying Cache or the cached value is stale (its age is greater
// than the refresh duration, see WithRefreshAfter) it will be looked up using the lookup function
// specified in NewCache.
//
// When the lookup of a stale value fails, Get will continue to return the old value.
func (l *Cache[T]) Get(ctx context.Context, key mem.RO) (val T, age time.Duration, ok bool) {
	hash := key.MapHash()
	idx := hash & uint64(len(l.groups)-1)
	return l.groups[idx].get(hash, key).wait(ctx)
}

func (l *Cache[T]) set(ctx context.Context, key mem.RO, val T, stats *Stats) {
	defer func() {
		if v := recover(); v != nil {
			stats.Panics++
			if l.opts.PanicHandler != nil {
				l.opts.PanicHandler(key, v)
			}
		}
	}()

	if err := l.c.Set(ctx, key, val); err != nil {
		stats.Errors++
		if l.opts.SetErrorHandler != nil {
			l.opts.SetErrorHandler(key, err)
		}
	}
}

// Running returns the number of running cache accesses and lookups.
func (l *Cache[T]) Running() int {
	var total int
	for i := range l.groups {
		s := &l.groups[i]
		s.mu.Lock()
		total += len(s.entries)
		s.mu.Unlock()
	}
	return total
}

// Stats returns statistics about all completed lookups and cache accesses.
func (l *Cache[T]) Stats() Stats {
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
// If err is ErrSkip the result will not be cached.
type Func[T any] func(ctx context.Context, key mem.RO) (val T, err error)

// ErrSkip can be returned by a Func to indicate that the result should not be cached.
var ErrSkip = errors.New("skipping result")

// Opts can be used to configure or change the behaviour of a Cache.
type Opts struct {
	// BackgroundRefresh enables refreshing of expired values in the background and serving of
	// stale values for values which are getting refreshed in the background.
	//
	// While a background refresh for a key all calls to [Cache.Get] will return the old, stale
	// value.
	BackgroundRefresh bool

	// ErrorHandler will be called when either the cache access or the lookup returns an error.
	//
	// If nil errors will be ignored.
	ErrorHandler func(key mem.RO, err error)

	// Timeout specifies the timeout for the initial cache access + lookup, ignoring the time
	// needed to put a value into the cache.
	//
	// If Timeout is <= 0 a default timeout of 250ms will be used.
	Timeout time.Duration

	// PanicHandler will be called whenever a panic occurs either when accessing the underlying
	// Cache or when looking up a value.
	//
	// If nil panics will be ignored.
	PanicHandler func(key mem.RO, v any)

	// RefreshAfter specifies the duration after which values in the cache will be treated
	// as missing and refreshed.
	//
	// If RefreshAfterJitterFunc is not nil it's result will be added to the value of RefreshAfter
	// before checking the age of a value.
	//
	// If the sum of RefreshAfter and the result of RefreshAfterJitterFunc is <= 0 no refresh will
	// be triggered.
	//
	// If a refresh fails the Cache will keep serving the old value and retry the refresh the
	// next time the underlying cache is checked.
	//
	// By default, a refresh will happen synchronously and the new value will be returned (unless
	// there was an error). This can be changed using BackgroundRefresh. See BackgroundRefresh for
	// more information.
	RefreshAfter time.Duration

	// RefreshAfterJitterFunc can be used to add jitter to the value specified in RefreshAfter.
	//
	// This can be useful to avoid thundering herd problems when many entries need to be
	// refreshed at the same time.
	//
	// The returned duration will be added to the value in RefreshAfter.
	//
	// If nil no jitter will be added.
	//
	// See RefreshAfter for more details.
	RefreshAfterJitterFunc func(key mem.RO) time.Duration

	// SetErrorHandler will be called when updating the underlying Cache fails.
	//
	// If nil errors will be ignored.
	SetErrorHandler func(key mem.RO, err error)

	// SetTimeout specifies the timeout for saving a new or updated value into the underlying Cache.
	//
	// If SetTimeout is <= 0 a default timeout of 250ms will be used.
	SetTimeout time.Duration
}

func (o Opts) shouldRefresh(key mem.RO, age time.Duration) bool {
	limit := o.RefreshAfter
	if o.RefreshAfterJitterFunc != nil {
		limit += o.RefreshAfterJitterFunc(key)
	}
	return limit > 0 && limit <= age
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

type lookupGroup[T any] struct {
	cache *Cache[T]

	mu      sync.Mutex
	entries map[uint64]*lookupGroupEntry[T]
	stats   Stats
}

func (lg *lookupGroup[T]) add(hash uint64, key mem.RO) *lookupGroupEntry[T] {
	lge := &lookupGroupEntry[T]{
		group: lg,

		hash: hash,
		key:  key,

		done: make(chan struct{}),
	}
	lg.entries[hash] = lge
	return lge
}

func (lg *lookupGroup[T]) get(hash uint64, key mem.RO) *lookupGroupEntry[T] {
	lg.mu.Lock()
	lge, ok := lg.entries[hash]
	if !ok {
		lge = lg.add(hash, key)
	}
	lg.mu.Unlock()

	if !ok {
		go lge.run()
	}

	return lge
}

func (lg *lookupGroup[T]) remove(lge *lookupGroupEntry[T]) {
	lg.mu.Lock()
	delete(lg.entries, lge.hash)
	lg.stats = lg.stats.add(lge.stats)
	lg.mu.Unlock()
}

type lookupGroupEntry[T any] struct {
	group *lookupGroup[T]
	hash  uint64
	key   mem.RO

	done  chan struct{}
	stats Stats

	fetchCtx lazyTimeoutContext
	setCtx   lazyTimeoutContext

	age    time.Duration
	closed bool
	ok     bool
	val    T
}

func (lge *lookupGroupEntry[T]) close() {
	if lge.closed {
		return
	}
	close(lge.done)
	lge.closed = true
}

func (lge *lookupGroupEntry[T]) closeAndRemove() {
	lge.close()
	lge.group.remove(lge)
}

func (lge *lookupGroupEntry[T]) handleErr(err error) {
	lge.stats.Errors++
	if lge.group.cache.opts.ErrorHandler != nil {
		lge.group.cache.opts.ErrorHandler(lge.key, err)
	}
}

func (lge *lookupGroupEntry[T]) handlePanic(v any) {
	lge.stats.Panics++
	if lge.group.cache.opts.PanicHandler != nil {
		lge.group.cache.opts.PanicHandler(lge.key, v)
	}
}

func (lge *lookupGroupEntry[T]) fetchCached(ctx context.Context) error {
	if lge.val, lge.age, lge.ok = lge.group.cache.c.Get(ctx, lge.key); lge.ok {
		return nil
	}
	return ctx.Err()
}

func (lge *lookupGroupEntry[T]) lookupAndCache(ctx context.Context) {
	lge.stats.Lookups++

	val, err := lge.group.cache.f(ctx, lge.key)
	if err != nil && !errors.Is(err, ErrSkip) {
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
		lge.close()
	}

	if lge.ok && err == nil {
		setCtx := &lge.setCtx
		setCtx.deadline = time.Now().Add(lge.group.cache.opts.SetTimeout)
		defer setCtx.cancel()

		lge.group.cache.set(setCtx, lge.key, val, &lge.stats)
	}
}

func (lge *lookupGroupEntry[T]) run() {
	defer func() {
		if v := recover(); v != nil {
			lge.handlePanic(v)
		}

		lge.closeAndRemove()
	}()

	ctx := &lge.fetchCtx
	ctx.deadline = time.Now().Add(lge.group.cache.opts.Timeout)
	defer ctx.cancel()

	if err := lge.fetchCached(ctx); err != nil {
		lge.handleErr(err)
		return
	}

	if !lge.ok {
		lge.stats.Misses++
	} else {
		lge.stats.Hits++

		if !lge.group.cache.opts.shouldRefresh(lge.key, lge.age) {
			return
		}

		lge.stats.Refreshes++

		if lge.group.cache.opts.BackgroundRefresh {
			lge.close()
		}
	}

	lge.lookupAndCache(ctx)
}

func (lge *lookupGroupEntry[T]) wait(ctx context.Context) (val T, age time.Duration, ok bool) {
	select {
	case <-ctx.Done():
		return
	case <-lge.done:
		return lge.val, lge.age, lge.ok
	}
}

type lazyTimeoutContext struct {
	deadline time.Time

	mu    sync.Mutex
	done  atomic.Value // of chan struct{}
	err   error
	timer *time.Timer
}

var closedChan = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

func (l *lazyTimeoutContext) cancel() {
	l.cancelWithError(context.Canceled)
}

func (l *lazyTimeoutContext) cancelWithError(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.cancelWithErrorLocked(err)
}

func (l *lazyTimeoutContext) cancelWithErrorLocked(err error) {
	if l.err != nil {
		return
	}

	done, _ := l.done.Load().(chan struct{})

	if done != nil {
		close(done)
	} else {
		l.done.Store(closedChan)
	}

	l.err = err

	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
}

func (l *lazyTimeoutContext) Deadline() (time.Time, bool) {
	return l.deadline, true
}

func (l *lazyTimeoutContext) Done() <-chan struct{} {
	done, _ := l.done.Load().(chan struct{})
	if done != nil {
		return done
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	done, _ = l.done.Load().(chan struct{})
	if done != nil {
		return done
	}

	timeLeft := time.Until(l.deadline)

	if timeLeft <= 0 {
		l.cancelWithErrorLocked(context.DeadlineExceeded)
		return closedChan
	}

	done = make(chan struct{})

	l.done.Store(done)
	l.timer = time.AfterFunc(timeLeft, func() { l.cancelWithError(context.DeadlineExceeded) })

	return done
}

func (l *lazyTimeoutContext) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *lazyTimeoutContext) Value(any) any {
	return nil
}
