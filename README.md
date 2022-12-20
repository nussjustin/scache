# scache [![Go Reference](https://pkg.go.dev/badge/github.com/nussjustin/scache.svg)](https://pkg.go.dev/github.com/nussjustin/scache) [![Lint](https://github.com/nussjustin/scache/workflows/Lint/badge.svg)](https://github.com/nussjustin/scache/actions?query=workflow%3ALint) [![Test](https://github.com/nussjustin/scache/workflows/Test/badge.svg)](https://github.com/nussjustin/scache/actions?query=workflow%3ATest) [![Go Report Card](https://goreportcard.com/badge/github.com/nussjustin/scache)](https://goreportcard.com/report/github.com/nussjustin/scache)

Package scache implements a simple LRU cache together with various convenience wrappers around Caches including a wrapper for sharding access to multiple Caches.

## Installation

scache uses [go modules](https://github.com/golang/go/wiki/Modules) and can be installed via `go get`.

```bash
go get github.com/nussjustin/scache
```

## Example

### Simple

Saving a value in a cache

```go
user := User{ID: 1, Name: "Gopher", Age: 12}

lru := scache.NewLRU[User](32)

if err := lru.Set(ctx, mem.S(strconv.Itoa(user.ID)), user); err != nil {
    log.Fatalf("failed to cache user %d: %s", user.ID, err)
}
```

Retrieving a value from the cache

```go
userID := getUserID(ctx)

// Ignore age of cache entry
user, _, ok := lru.Get(ctx, mem.S(strconv.Itoa(userID)))
if !ok {
    log.Fatalf("user with ID %d not found in cache", userID)
}

fmt.Printf("%+v\n", user)
```

### Lookup Cache

Initializing cache

```go
c := lookup.NewCache[string](
	scache.NewLRU[string](32),
	func(ctx context.Context, key mem.RO) (val string, err error) {
		return strings.ToUpper(key.StringCopy()), nil
	},
	&lookup.Opts{Timeout: 5*time.Second},
)
```

Looking up value

```go
val, _, _ := c.Get(context.Background(), mem.S("hello"))
if val != "HELLO" {
    panic("something went wrong... PANIC")
}
```

## Contributing
Pull requests are welcome. For major changes, please open an issue first to discuss what you would like to change.

Please make sure to update tests as appropriate.

## License
[MIT](https://choosealicense.com/licenses/mit/)
