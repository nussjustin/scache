# scache [![Go Reference](https://pkg.go.dev/badge/github.com/nussjustin/scache.svg)](https://pkg.go.dev/github.com/nussjustin/scache) [![Lint](https://github.com/nussjustin/scache/workflows/Lint/badge.svg)](https://github.com/nussjustin/scache/actions?query=workflow%3ALint) [![Test](https://github.com/nussjustin/scache/workflows/Test/badge.svg)](https://github.com/nussjustin/scache/actions?query=workflow%3ATest) [![Go Report Card](https://goreportcard.com/badge/github.com/nussjustin/scache)](https://goreportcard.com/report/github.com/nussjustin/scache)

Package scache implements a simple cache interface with support for cache tags and lookup caching.

## Installation

scache uses [go modules](https://github.com/golang/go/wiki/Modules) and can be installed via `go get`.

```bash
go get github.com/nussjustin/scache
```

## Example

### Manual fetching and saving

To fetch a value from the cache, use the [Get][0] method and pass the cache key and a context.

If the item was found ([Item.Hit][1] is true) the returned [Item][2] will hold the cached value in [Item.Value][3].

Example:

```go
var cache = scache.New[*User](scache.NewLRU(32), nil)

func getUser(ctx context.Context, id string) (*User, error) {
    item, err := cache.Get(ctx, id)
    if err != nil {
        return nil, err
    }
    // User was not found in the case
    if !item.Hit {
        return nil, nil
    }
    return item.Value, nil
}
```

A more realistic example would fall back to loading the data from an external system on cache miss and than try to cache
the loaded data into the cache.

The following example shows how this can be done manually using [Get][0] and [Set][4].

The example also shows another good practice when working with caches: Ignoring failed gets / sets. This way the cache
becomes purely optional and a problem with the cache (for example a failed remote cache) will only cause higher
operation latency instead of failing operations right out.

```go
var cache = scache.New[*User](scache.NewLRU(32), nil)

func getUser(ctx context.Context, id string) (*User, error) {
    item, err := cache.Get(ctx, id)
    if err != nil {
        // Fall back to loading the user when there is a problem with the cache
        return loadUser(ctx, id)
    }

    if item.Hit {
        return item.Value, nil
    }

    // On cache miss load the value from external storage
    user, err := loadUser(ctx, id)
    if err != nil {
        return nil, err
    }

    if user != nil {
        // Cache the value but ignore any errors, as we can still return the value even if we fail to cache it.
        _ = cache.Set(ctx, id, scache.Value(user))
    }

    return user, nil
}
```

### Loading Values

Since the previous example is very common, a convenience method [Load][5] is provided that abstracts away the whole
get + load/compute + set logic and allows users to only care about the actual logic for loading/computing the cached value.

[Load][5] will try to fetch a value from the cache first and fall back to computing it on miss, caching the result
automatically.

The previous example could be rewritten using [Load][5] like this:

```go
var cache = scache.New[*User](scache.NewLRU(32), nil)

func getUser(ctx context.Context, id string) (*User, error) {
    item, err := cache.Load(ctx, id, func(ctx context.Context, id string) (scache.Item[*User], error) {
        user, err := loadUser(ctx, id)
        // scache.Value is a shorthand for sache.Item[*User]{Value: user}
        return scache.Value(user), err
    })

    // Here an error means that we do not have a value and could not load one, so we just return it
    if err != nil {
        return nil, err
    }

    // If there was no error the item was either loaded from the cache, in which case item.Hit is true, or newly loaded
    // in which case item.Hit is false. What case exactly does not matter here and we simply return the value as is.
    return item.Value
}
```

#### Stale data

Additionally [Load][5] optionally also allows working with _stale_ data: Values that are technically expired, defined
via the [Item.ExpiresAt][6] field, but that can still be used for some time, if a new value can not be provided.

An example where this could happen is when fetching data from an external API fails, because the API is currently
unavailable.

In this case [Load][5] can automatically return the _stale_ value.

Handling of expired values as stale can be enabled using the [CacheOpts.StaleDuration][7] field which defines a duration
during which expired values can still be used.

This can look like this:

```go
var cache = scache.New[*User](scache.NewLRU(32), &scache.CacheOpts{
    // Allow serving of stale values for 5 minutes
    StaleDuration: 5 * time.Minute,
})

func getUser(ctx context.Context, id string) (*User, error) {
    item, err := cache.Load(ctx, id, func(ctx context.Context, id string) (scache.Item[*User], error) {
        user, err := loadUser(ctx, id)

        // Mark the item as valid for 5 minutes
        item := scache.Value(user)
        item.ExpiresAt = time.Now().Add(5 * time.Minute)

        return item, err
    })

    // With stale values allowed, we may still have an item even if there was an error.
    //
    // If item.Hit is true, it means we got a value from the cache before which had already expired
    // (current time > item.ExpiresAt), so we tried to use loadUser to load the user from external storage, but there
    // was an error.
    //
    // Instead of returning nothing, Load returned the cached, but expired value since it is still in our 5 minutes
    // stale window (current time < item.ExpiresAt + 5 minutes)
    if err != nil {
        return item, err
    }

    return item.Value
}
```

The only difference is that `item` may contain a valid, but stale, cached value even if `err != nil`, so we return
`item` in the `if err != nil` case instead of `nil`.

#### Collecting cache tags

Cache tags can be used by cache implementations to manage cache items and to allow for precise pruning of old data,
without having to know every cache key.

In addition to manually settings the [Item.Tags][8] fields during [Load][5] it is also possible to collect cache tags
using the global [Tag][9] and [Tags][10] functions and passing the `context.Context` given to the callback in [Load][5].

This can be useful for situations where a value depends on certain configurations or other data, but manually passing
around and filling a list with tags is either to cumbersome or simply not possible.

A good example would be logic that depends on global configuration that can change at any time. If the configuration
changes, the result of a computation may be different and so using an existing cached value may cause unnecessary delays
in seeing the expected, new result or even cause errors.

Instead of removing all items from the cache, or all that access any configuration, cache tags can be used to only
remove items from the cache that actually depend on the changed configuration.

An example of rendering a users account page with either an existing, old design or a new design based on global
configuration. By using the global [Tag][9] function it becomes
possible to automatically removed the cached entry when the used configuration changes.

```go
var cache = scache.New[string](scache.NewLRU(32), nil)

func isConfigEnabled(ctx context.Context, name string) bool {
    // Get value from global configuration
    enabled := globalConfiguration.GetBool(name)

    // Add tag for the configuration we used
    scache.Tag(ctx, "config:" + name)

    return enabled
}

func renderAccountDetails(ctx context.Context, user *User) (string, error) {
    item, err := cache.Load(ctx, user.ID, func(ctx context.Context, id string) (scache.Item[string], error) {
        var result string
        var err error

        // This will automatically add the tag using scache.Tag.
        if isConfigEnabled(ctx, "use-new-design") {
            result, err = renderNewAccountDetails(ctx, user)
        } else {
            result, err = renderOldAccountDetails(ctx, user)
        }

        // No need to manually specify any tags
        return scache.Value(result), err
    })

    if err != nil {
        return item, nil
    }

    // Here item.Tags contains "config:use-new-design"!

    return item.Value
}
```

When doing nested calls to [Load][5], cache tags added to an item, either via [Tag][9]/[Tags][10] or directly by adding
them to [Item.Tags][8], are also automatically added to the result of the other [Load][5] calls.

This provides a form of dependency management that can allow for easier and more precise pruning / updating of cached
items.

Building on the previous example:

```go
var fullPageCache = scache.New[string](scache.NewLRU(32), nil)

func renderAccountPage(ctx context.Context, user *User) (string, error) {
    item, err := fullPageCache.Load(ctx, user.ID, func(ctx context.Context, id string) (scache.Item[string], error) {
        header, err := renderHeader(ctx)
        if err != nil {
            return scache.Value(""), err
        }
        body, err := renderAccountDetails(ctx, user)
        if err != nil {
            return scache.Value(""), err
        }
        footer, err := renderHeader(ctx)
        if err != nil {
            return scache.Value(""), err
        }
        return header + body + footer
    })

    if err != nil {
        return item, nil
    }

    // Here item.Tags also contains "config:use-new-design"!

    return item.Value
}
```

The item returned by `renderAccountPage` automatically has the same `"config:use-new-design"` tag from our previous
example.

### Synchronized loading of values (singleflight)

If multiple goroutines are trying to load the same data concurrently, for example in a web application where multiple
users are accessing the same endpoints at the same time, those goroutines may end up all doing the same work for
computing/loading the data (on cache miss) and save it to the cache, even though the result will always be the same for
all goroutines.

Instead of doing the same work multiple times, it may make more sense to do it only once and share the result between
all goroutines.

This is a very common situation and the Go team even provides a package called [golang.org/x/sync/singleflight][11]
that implements a solution for this.

While the [golang.org/x/sync/singleflight][11] package can be used together with scache, scache also provides its own
implementation of this idea, which allows users to avoid the dependency while also providing better control of the
caching behaviour without requiring the user to write extra code.

The scache implementation of this pattern is available via the [LoadSync] method. This method has the same signature
as [Load] and uses the same basic logic, but guarantees that all goroutines that for every key, only one goroutine
is executing the get + load/compute + set logic at any time.

## Contributing
Pull requests are welcome. For major changes, please open an issue first to discuss what you would like to change.

Please make sure to update tests as appropriate.

## License
[MIT](https://choosealicense.com/licenses/mit/)

[0]: https://pkg.go.dev/github.com/nussjustin/scache#Cache.Get
[1]: https://pkg.go.dev/github.com/nussjustin/scache#Item.Hit
[2]: https://pkg.go.dev/github.com/nussjustin/scache#Item
[3]: https://pkg.go.dev/github.com/nussjustin/scache#Cache.Value
[4]: https://pkg.go.dev/github.com/nussjustin/scache#Cache.Set
[5]: https://pkg.go.dev/github.com/nussjustin/scache#Cache.Load
[6]: https://pkg.go.dev/github.com/nussjustin/scache#Item.ExpiresAt
[7]: https://pkg.go.dev/github.com/nussjustin/scache#CacheOpts.StaleDuration
[8]: https://pkg.go.dev/github.com/nussjustin/scache#Item.Tags
[9]: https://pkg.go.dev/github.com/nussjustin/scache#Tag
[10]: https://pkg.go.dev/github.com/nussjustin/scache#Tags
[11]: https://pkg.go.dev/golang.org/x/sync/singleflight