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

// NewCache returns a new *Cache using c as the underlying Cache and f for
// looking up missing values.
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

	l := &Cache[T]{
		opts: *opts,

		c: c,
		f: f,

		groups: make([]lookupGroup[T], 256),
	}
	for i := range l.groups {
		l.groups[i] = lookupGroup[T]{
			cache:   l,
			entries: make(map[uint64]*lookupGroupEntry[T]),
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

var (
	ErrSkip = errors.New("skipping result")
)

// Opts can be used to configure or change the behaviour of a Cache.
type Opts struct {
	// BackgroundRefresh enables refreshing of expired values in the background and serving of stale values for values
	// which are getting refreshed in the background.
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
	PanicHandler func(key mem.RO, v interface{})

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
	SetErrorHandler func(key mem.RO, err error)

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

type lookupGroup[T any] struct {
	cache *Cache[T]

	mu       sync.Mutex
	entries  map[uint64]*lookupGroupEntry[T]
	stats    Stats
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
	sge, ok := lg.entries[hash]
	if !ok {
		sge = lg.add(hash, key)
	}
	lg.mu.Unlock()

	if !ok {
		go sge.run()
	}

	return sge
}

func (lg *lookupGroup[T]) remove(sge *lookupGroupEntry[T]) {
	lg.mu.Lock()
	delete(lg.entries, sge.hash)
	lg.stats = sge.group.stats.add(sge.stats)
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

func (lge *lookupGroupEntry[T]) handlePanic(v interface{}) {
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
		setCtx.t = time.Now().Add(lge.group.cache.opts.SetTimeout)
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
	ctx.t = time.Now().Add(lge.group.cache.opts.Timeout)
	defer ctx.cancel()

	if err := lge.fetchCached(ctx); err != nil {
		lge.handleErr(err)
		return
	}

	if !lge.ok {
		lge.stats.Misses++
	} else {
		lge.stats.Hits++

		if !lge.group.cache.opts.shouldRefresh(lge.age) {
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
	t     time.Time
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

func (l *lazyTimeoutContext) Deadline() (deadline time.Time, ok bool) {
	return l.t, true
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

	d := time.Until(l.t)

	if d <= 0 {
		l.cancelWithErrorLocked(context.DeadlineExceeded)
		return closedChan
	}

	done = make(chan struct{})

	l.done.Store(done)
	l.timer = time.AfterFunc(d, func() { l.cancelWithError(context.DeadlineExceeded) })

	return done
}

func (l *lazyTimeoutContext) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *lazyTimeoutContext) Value(interface{}) interface{} {
	return nil
}
