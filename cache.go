package scache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNotFound is a marker value that is returned by [Adapter] implementations on a miss.
var ErrNotFound = errors.New("value not found")

// Adapter defines methods for storing and retrieving loaded values.
//
// Implementations must be safe for concurrent access.
type Adapter[K, V any] interface {
	// Get returns the value for the given key together with its age.
	//
	// If a value was not found, a value that is ErrNotFound must be returned.
	Get(ctx context.Context, key K) (value V, age time.Duration, err error)

	// Set stores the given value in the cache.
	//
	// The error returned by this method is ignored by Cache.
	Set(ctx context.Context, key K, value V) error
}

// Func is the type for functions used to load values.
type Func[K, V any] func(ctx context.Context, key K) (V, error)

// Option is used to configure a [Cache].
type Option func(*options)

type options struct {
	contextFunc    func() (context.Context, context.CancelFunc)
	jitterFunc     func() time.Duration
	maxStale       time.Duration
	refreshStale   bool
	staleAfter     time.Duration
	timerFunc      func(d time.Duration) <-chan time.Time
	waitForRefresh time.Duration
}

func (o *options) isFresh(age time.Duration) bool {
	return o.staleAfter < 0 || age < o.staleAfter
}

func (o *options) isStale(age time.Duration) bool {
	if o.staleAfter < 0 {
		return false
	}

	staleFor := age - o.staleAfter

	return staleFor >= 0 && (o.maxStale < 0 || staleFor < o.maxStale)
}

// WithContextFunc specifies the function used to create the internal [context.Context] that is passed to the [Func].
//
// If not specified, the default is a new context based on the background context.
func WithContextFunc(f func() (context.Context, context.CancelFunc)) Option {
	return func(o *options) {
		o.contextFunc = f
	}
}

// WithJitterFunc sets a function used to add jitter to staleness checks.
//
// This can be used to avoid the thundering herd problem, where many values are reloaded at the same time.
//
// The function will be called each time the staleness of a value is checked and the result will be added to the actual
// age reported by the [Adapter].
func WithJitterFunc(f func() time.Duration) Option {
	return func(o *options) {
		o.jitterFunc = f
	}
}

// WithMaxStale defines the amount of time a stale value can still be used.
//
// If a value has been stale for this duration or longer, the value will be ignored as if there was no value.
//
// A negative value will allow stale values to be used forever.
func WithMaxStale(d time.Duration) Option {
	return func(o *options) {
		o.maxStale = d
	}
}

// WithRefreshStale configures whether stale values should be automatically refreshed.
func WithRefreshStale(refresh bool) Option {
	return func(o *options) {
		o.refreshStale = refresh
	}
}

// WithStaleAfter defines the duration after which a cached value should be considered stale.
//
// A negative value will disable staleness entirely.
func WithStaleAfter(d time.Duration) Option {
	return func(o *options) {
		o.staleAfter = d
	}
}

// WithTimerFunc sets the function used to create timers used when waiting for a refresh. See [WithWaitForRefresh].
//
// If not specified, the default function is [time.After].
func WithTimerFunc(f func(d time.Duration) <-chan time.Time) Option {
	return func(o *options) {
		o.timerFunc = f
	}
}

// WithWaitForRefresh sets the duration for which [Cache.Get] should wait for a running background reload, if there
// is one, before falling back to returning a stale, value.
func WithWaitForRefresh(d time.Duration) Option {
	return func(o *options) {
		o.waitForRefresh = d
	}
}

// Cache provides a way to load a value based on a potentially cached key in a way that multiple concurrent calls for
// the same key will only load the value once, with support for stale values and refreshing cached values in the
// background.
type Cache[K comparable, V any] struct {
	adapter Adapter[K, V]
	fun     Func[K, V]
	opts    options

	callsMu sync.Mutex
	// TODO: Use maphash.Comparable in Go 1.24 to shard the map
	calls map[K]*call[V]
}

type call[V any] struct {
	done chan struct{}

	val V
	err error
}

// New returns a new Cache using the given function to calculate / load values.
//
// By default, values are never considered stale or expired. See [WithStaleAfter] and [WithMaxStale] for configuring
// staleness and expiration.
func New[K comparable, V any](adapter Adapter[K, V], fun Func[K, V], opts ...Option) *Cache[K, V] {
	o := options{
		contextFunc: func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		},
		jitterFunc: func() time.Duration {
			return 0
		},
		maxStale:       -1,
		staleAfter:     -1,
		timerFunc:      time.After,
		waitForRefresh: 0,
	}

	for _, opt := range opts {
		opt(&o)
	}

	return &Cache[K, V]{
		adapter: adapter,
		fun:     fun,
		opts:    o,

		calls: make(map[K]*call[V]),
	}
}

// Get returns the value for the given key or an error.
//
// The value may come from the cache, in which case it may be stale, depending on the configuration.
//
// If multiple goroutines call Get at the same time with the same key, the value will be loaded only once.
func (l *Cache[K, V]) Get(ctx context.Context, key K) (V, error) {
	if err := ctx.Err(); err != nil {
		var zero V
		return zero, err
	}

	cached, age, err := l.adapter.Get(ctx, key)

	if err != nil && !errors.Is(err, ErrNotFound) {
		var zero V
		return zero, err
	}

	miss := err != nil

	var ageWithJitter time.Duration

	if !miss {
		ageWithJitter = age + l.opts.jitterFunc()
	}

	var load *call[V]
	var timerC <-chan time.Time

	switch {
	case miss:
		load = l.load(key)
	case l.opts.isFresh(ageWithJitter):
		return cached, nil
	case l.opts.isStale(ageWithJitter) && l.opts.refreshStale:
		load = l.load(key)

		if l.opts.waitForRefresh <= 0 {
			return cached, nil
		}

		// No need to wait for the refresh if the value is still fresh when ignoring our jitter
		if l.opts.isFresh(age) {
			return cached, nil
		}

		timerC = l.opts.timerFunc(l.opts.waitForRefresh)
	case l.opts.isFresh(age) || l.opts.isStale(age):
		return cached, nil
	default: // expired
		load = l.load(key)
	}

	select {
	case <-ctx.Done():
		var zero V
		return zero, ctx.Err()
	case <-load.done:
		return load.val, load.err
	case <-timerC:
		return cached, nil
	}
}

func (l *Cache[K, V]) load(key K) *call[V] {
	l.callsMu.Lock()
	defer l.callsMu.Unlock()

	c := l.calls[key]
	if c != nil {
		return c
	}

	c = &call[V]{done: make(chan struct{})}

	l.calls[key] = c

	go func() {
		defer func() {
			l.callsMu.Lock()
			defer l.callsMu.Unlock()

			delete(l.calls, key)
		}()

		defer func() {
			select {
			// If we got a fresh value the channel will be closed already
			case <-c.done:
			default:
				close(c.done)
			}
		}()

		ctx, cancel := l.opts.contextFunc()
		defer cancel()

		func() {
			defer func() {
				if v := recover(); v != nil {
					c.err = wrapRecovered(v)
				}
			}()

			c.val, c.err = l.fun(ctx, key)
		}()

		// Close immediately so that we do not block on the cache update
		close(c.done)

		if c.err != nil {
			return
		}

		// Ignore the error. If the user cares about this they can just handle the error in the cache implementation.
		_ = l.adapter.Set(ctx, key, c.val)
	}()

	return c
}

func wrapRecovered(v any) error {
	if err, ok := v.(error); ok {
		return err
	}

	return fmt.Errorf("recovered panic: %v", v)
}
