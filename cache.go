package scache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nussjustin/scache/internal/testutil"
)

// Backend defines a low-level interface for interacting with caches.
type Backend[T any] interface {
	// Get returns the value associated with the given key from the cache.
	//
	// The returned value may already be expired.
	Get(ctx context.Context, key string) (Item[T], error)

	// Set updates the cache for the given key with a new value.
	Set(ctx context.Context, key string, item Item[T]) error
}

// TODO: LRU Backend
// TODO: Redis Backend
// TODO: Multi Level Backend
// TODO: Backend that saves in background with timeout

// Cache provides a high-level cache interface on top of a lower-level [Backend] implementation.
type Cache[T any] struct {
	b       Backend[T]
	opts    CacheOpts
	getMany func(ctx context.Context, keys ...string) ([]Item[T], error)

	syncMu sync.Mutex
	sync   map[string]func() loadSyncResult[T]
}

// CacheOpts defines options that can be used to customize how a [Cache] behaves.
type CacheOpts struct {
	// IgnoreGetErrors causes errors during the cache lookup inside [Cache.Load] and [Cache.LoadSync] to be ignored.
	//
	// If an error occurs, the code will treat the error as a simple cache miss and continue with calling the load
	// callback.
	IgnoreGetErrors bool

	// LoadSyncContextFunc is called by LoadSync to get a context for the call of the [Loader].
	//
	// If not set the following default is used:
	//
	// 	   func() (context.Context, context.CancelFunc) {
	//	       return context.WithCancel(context.Background())
	//	   }
	//
	LoadSyncContextFunc func() (context.Context, context.CancelFunc)

	// SinceFunc is used to calculate relative differences from a given time to the current time.
	//
	// If nil, time.Since is used.
	SinceFunc func(time.Time) time.Duration

	// StaleDuration defines an optional duration during which expired cache entries are considered as stale.
	//
	// Stale values will be treated as normal hits by [Cache.Get] and [Cache.GetMany], while [Cache.Load] and
	// [Cache.LoadSync] will consider these values as a miss, unless there was an error, in which case the stale value
	// is returned instead of the error.
	StaleDuration time.Duration
}

type loadSyncResult[T any] struct {
	item Item[T]
	err  error
}

func (c *CacheOpts) value() CacheOpts {
	if c == nil {
		return CacheOpts{}
	}
	return *c
}

// New returns a new [Cache] using the given [Backend].
//
// If given, opts can be used to change the behaviour of the [Cache]. Any changes to the value pointed to by [opts]
// that are made after the call to New are ignored.
//
// Optionally, if the given backend implements a method called GetMany with the signature
//
//	GetMany(ctx context.Context, keys ...string) ([]Item[T], error)
//
// the returned cache will use this method to implement its [Cache.GetMany] method. If the backend does not implement
// this method, the cache will fall back to sequentially fetching items. As an optimization, if no keys are given to
// [Cache.GetMany], the GetMany implementation of the backend will not be called, even if provided.
func New[T any](b Backend[T], opts *CacheOpts) *Cache[T] {
	c := &Cache[T]{b: b, opts: opts.value()}
	c.getMany = getManyFunc(c)
	c.sync = make(map[string]func() loadSyncResult[T])

	if c.opts.LoadSyncContextFunc == nil {
		c.opts.LoadSyncContextFunc = func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}
	}

	return c
}

func getManyFunc[T any](c *Cache[T]) func(context.Context, ...string) ([]Item[T], error) {
	if bm, ok := c.b.(interface {
		GetMany(context.Context, ...string) ([]Item[T], error)
	}); ok {
		return bm.GetMany
	}

	return func(ctx context.Context, keys ...string) ([]Item[T], error) {
		items := make([]Item[T], len(keys))
		for i, key := range keys {
			item, err := c.b.Get(ctx, key)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", key, err)
			}
			if item.Hit && item.ExpiresAt.IsZero() || c.opts.StaleDuration <= 0 || c.opts.StaleDuration > c.since(item.ExpiresAt) {
				items[i] = item
			}
		}
		return items, nil
	}
}

func (c *Cache[T]) expired(expiresAt time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return c.since(expiresAt) > 0
}

func (c *Cache[T]) since(t time.Time) time.Duration {
	if c.opts.SinceFunc == nil {
		return time.Since(t)
	}
	return c.opts.SinceFunc(t)
}

func (c *Cache[T]) stale(expiresAt time.Time) bool {
	if expiresAt.IsZero() || c.opts.StaleDuration <= 0 {
		return false
	}
	d := c.since(expiresAt)
	return d > 0 && d < c.opts.StaleDuration
}

// Get returns the item for the given key from the underlying backend.
func (c *Cache[T]) Get(ctx context.Context, key string) (Item[T], error) {
	item, err := c.b.Get(ctx, key)
	if err != nil {
		var zero Item[T]
		return zero, err
	}
	if item.Hit && c.expired(item.ExpiresAt) && !c.stale(item.ExpiresAt) {
		item = Item[T]{}
	}
	return item, nil
}

// GetMany returns the items for the given from the underlying backend.
//
// The returned slice will have one item per key given. If no keys are given, the slice will be nil.
//
// GetMany returns on the first error encountered.
//
// Some backends may have an optimized implementation for this. See [New] for more on this topic.
func (c *Cache[T]) GetMany(ctx context.Context, keys ...string) ([]Item[T], error) {
	if len(keys) == 0 {
		return nil, nil
	}
	return c.getMany(ctx, keys...)
}

func (c *Cache[T]) load(ctx context.Context, key string, load Loader[T]) (Item[T], error) {
	item, err := c.b.Get(ctx, key)
	if err != nil && !c.opts.IgnoreGetErrors {
		return Item[T]{}, err
	}
	if item.Hit && !c.expired(item.ExpiresAt) {
		return item, nil
	}

	t := &tagger{}

	loaded, err := load(context.WithValue(ctx, taggerKey, t), key)
	if err != nil {
		if !item.Hit || !c.stale(item.ExpiresAt) {
			item = Item[T]{}
		}
		return item, err
	}

	// Always change Hit to false, so that users can distinguish between cached and uncached values
	loaded.Hit = false

	if !t.tags.empty() {
		for _, tag := range loaded.Tags {
			t.add(tag)
		}

		loaded.Tags = t.all()
	}

	parentT, _ := ctx.Value(taggerKey).(*tagger)
	parentT.addAll(loaded.Tags)

	_ = c.b.Set(ctx, key, loaded)

	return loaded, nil
}

// Load loads the value for the given key from the cache and returns it or, if the value is missing, the given load
// function will be called instead and the result will be cached, if no error occurred.
//
// An [Item] returned by the load function will have its [Item.Hit] field set to false, making it possible to
// distinguish between cached and uncached values.
//
// An error when looking up the existing value from the cache will cause that error to be returned, unless the
// [CacheOpts.IgnoreGetErrors] option was set. In this case the error will be ignored and handled as a cache miss.
//
// Expired values from the cache are treated as missing and will cause a new value to be loaded. If loading fails and
// the expired value is inside the staleness windows (see [CacheOpts.StaleDuration]) it will be returned _together_ with
// the error from the callback.
//
// If there is an error saving the loaded value to the cache, that error will be ignored.
//
// When load is called, a new child context with its own "cache scope" will be passed. During the call to load, the
// global [Tag] and [Tags] functions can be used to add tags to the current cache scope associated with the context.
//
// When saving the generated value, all added tags will also be passed to the backend.
//
// Nested calls to Load will automatically create nested cache scopes. When adding a tag to a nested cache scope, the
// tags will also be added to all outer cache scopes.
func (c *Cache[T]) Load(ctx context.Context, key string, load Loader[T]) (Item[T], error) {
	return c.load(ctx, key, load)
}

// LoadSync works like [Cache.Load] but guarantees that for each unique key, only one callback is executed at a time.
//
// If multiple goroutines call LoadSync at the same time with the same key, they will all wait for the result of the
// one running load callback.
//
// When calling the load function, a new context with separate timeout is passed (see [CacheOpts.LoadSyncTimeout]).
func (c *Cache[T]) LoadSync(ctx context.Context, key string, load Loader[T]) (Item[T], error) {
	var loadCtx context.Context
	var cancel context.CancelFunc

	c.syncMu.Lock()
	once := c.sync[key]
	if once == nil {
		once = sync.OnceValue(func() loadSyncResult[T] {
			defer func() {
				c.syncMu.Lock()
				delete(c.sync, key)
				c.syncMu.Unlock()
			}()

			loadCtx, cancel = c.opts.LoadSyncContextFunc()
			defer cancel()

			item, err := c.load(loadCtx, key, load)

			return loadSyncResult[T]{item, err}
		})

		c.sync[key] = once
	}
	c.syncMu.Unlock()

	// Used by tests to ensure that two goroutines do not race
	testutil.LoadSync(c)

	result := once()

	Tags(ctx, result.item.Tags...)

	return result.item, result.err
}

// Set updates the cache for the given key with a new value.
func (c *Cache[T]) Set(ctx context.Context, key string, item Item[T]) error {
	return c.b.Set(ctx, key, item)
}

type tagger struct {
	mu   sync.Mutex
	tags stringSet
}

var taggerKey = &struct{ _ byte }{}

func (t *tagger) add(tag string) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.tags.add(tag)
}

func (t *tagger) addAll(tags []string) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for _, tag := range tags {
		t.tags.add(tag)
	}
}

func (t *tagger) all() []string {
	if t == nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	return t.tags.all()
}

// Tag adds the given tag to the current cache scope and all its parents.
//
// If ctx has no associated cache scope, this is a no-op.
//
// See [Cache.Load] for more information on caches scopes.
func Tag(ctx context.Context, tag string) {
	t, _ := ctx.Value(taggerKey).(*tagger)
	t.add(tag)
}

// Tags adds the given tags to the current cache scope and all its parents.
//
// If ctx has no associated cache scope, this is a no-op.
//
// See [Cache.Load] for more information on caches scopes.
func Tags(ctx context.Context, tags ...string) {
	t, _ := ctx.Value(taggerKey).(*tagger)
	t.addAll(tags)
}

// Item represents a single cached value with all its metadata or a cache miss if [Item.Hit] is false.
type Item[T any] struct {
	// CachedAt contains the time at which the item was saved.
	//
	// This may not be supported by all caches.
	CachedAt time.Time

	// ExpiresAt specifies the time at which the item will expire in the cache.
	//
	// Some cache backends may not remove expired values automatically, in which case this may be a value in the past.
	//
	// This may not be supported by all caches.
	ExpiresAt time.Time

	// Hit is true if the value was found in the cache, even if may be stale (ExpiresAt is in the past).
	//
	// This is ignored when saving items to the cache.
	Hit bool

	// Tags is an unsorted list of tags associated with this item.
	//
	// This may not be supported by all caches.
	//
	// The slice must not be modified by users so that backends can keep a reference to it without creating a copy.
	Tags []string

	// Value holds the cached value if there was a hit, or the zero value of T otherwise.
	Value T

	// Weight is an optional weight value that can be used by caches for example to limit the number of hold items.
	Weight uint
}

// Value is a shorthand for scache.Item[T]{Tags: tags, Value: v}.
func Value[T any](v T, tags ...string) Item[T] {
	return Item[T]{Tags: tags, Value: v}
}

// Loader defines the signature for functions that can be passed to [Cache.Load].
type Loader[T any] func(ctx context.Context, key string) (Item[T], error)
