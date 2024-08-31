# scache [![Go Reference](https://pkg.go.dev/badge/github.com/nussjustin/scache.svg)](https://pkg.go.dev/github.com/nussjustin/scache) [![Lint](https://github.com/nussjustin/scache/workflows/Lint/badge.svg)](https://github.com/nussjustin/scache/actions?query=workflow%3ALint) [![Test](https://github.com/nussjustin/scache/workflows/Test/badge.svg)](https://github.com/nussjustin/scache/actions?query=workflow%3ATest) [![Go Report Card](https://goreportcard.com/badge/github.com/nussjustin/scache)](https://goreportcard.com/report/github.com/nussjustin/scache)

Package scache implements caching of computed values using callbacks.

## Installation

scache uses [go modules](https://github.com/golang/go/wiki/Modules) and can be installed via `go get`.

```bash
go get github.com/nussjustin/scache
```

## Example

### Basic usage

A cache can be created using the [scache.New][0] function.

The function takes a cache adapter, responsible for storing and retrieving values, and a function used to calculate new,
or refresh old (stale), values.

**Example:**

```go
func loadUser(ctx context.Context, id int) (*User, error) {
	// load user from database
}

var loadUserCached = scache.New[int, *User](scache.NewLRU(32), loadUser).Get

func main() {
	user, err := loadUserCached(context.Background(), 1)
	if err != nil {
		log.Fatalf("failed to get user: %s", err)
	}
	log.Println(user)
}
```

### Staleness

By default, once a value has been cached it will always be returned directly from the cache, but it is possible to
treat values with a certain age as _stale_ or _expired_ in which case they can be automatically refreshed.

To configure when a value is considered stale, the [scache.WithStaleAfter][1] option can be used. The given duration
must be greater than or equal to 0. Any cached value that is as old or older than the configured duration, will be
considered as stale.

Additionally, the [scache.WithMaxStale][2] option can be used to configure how long a value may be stale before it
should be considered as _expired_. An expired value is treated like a cache miss. If not specified, stale items are
never considered expired.

**Example**:

```go
var loadUserCached = scache.New[int, *User](scache.NewLRU(32), loadUser,
	scache.WithMaxStale(3 * time.Second),
	scache.WithStaleAfter(time.Second),
).Get
```

### Refreshing stale values

It is possible to have stale values be refreshed automatically in the background. This can be useful to ensure that
values don't get to old, without forcing callers to wait for the new value to be loaded.

This can be enabled using [scache.WithRefreshStale][3].

**Example**:

```go
var loadUserCached = scache.New[int, *User](scache.NewLRU(32), loadUser,
	scache.WithMaxStale(3 * time.Second),
	scache.WithRefreshStale(true),
	scache.WithStaleAfter(time.Second),
).Get
```

Additionally, the [scache.WithWaitForRefresh][4] option can be used to wait for a refresh to finish and return the fresh
value. The option takes a duration, which is used to limit how long to wait. If the refresh does not finish in the
configured time, the old, stale value will be returned instead.

### Protecting against the thundering herd problem

The thundering herd problem occurs if multiple cache entries have to be refreshed at the same time, potentially causing
create load on the system.

To reduce the change of this happening, it is possible to add _jitter_ to the staleness check. The jitter is
dynamically calculated and added to the age of each cached value and can be used to have values be refreshed sooner or
later than others, even if they were cached at around the same time, thus spreading out the load over time.

This can be configured using the [scache.WithJitterFunc][5] option, which takes a function that must return the jitter.

**Example**:

```go
var loadUserCached = scache.New[int, *User](scache.NewLRU(32), loadUser,
	scache.WithJitterFunc(func() time.Duration { return rand.N(time.Second) }),
	scache.WithMaxStale(3 * time.Second),
	scache.WithRefreshStale(true),
	scache.WithStaleAfter(time.Second),
).Get
```

### Forcing 

## Contributing
Pull requests are welcome. For major changes, please open an issue first to discuss what you would like to change.

Please make sure to update tests as appropriate.

## License
[MIT](https://choosealicense.com/licenses/mit/)

[0]: https://pkg.go.dev/github.com/nussjustin/scache/#New
[1]: https://pkg.go.dev/github.com/nussjustin/scache/#WithStaleAfter
[2]: https://pkg.go.dev/github.com/nussjustin/scache/#WithMaxStale
[3]: https://pkg.go.dev/github.com/nussjustin/scache/#WithRefreshStale
[4]: https://pkg.go.dev/github.com/nussjustin/scache/#WithWaitForRefresh
[5]: https://pkg.go.dev/github.com/nussjustin/scache/#WithJitterFunc
