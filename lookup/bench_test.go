package lookup_test

import (
	"context"
	"testing"
	"time"

	"github.com/nussjustin/scache/lookup"
)

type noopCache struct{}

func (n noopCache) Get(context.Context, string) (val interface{}, age time.Duration, ok bool) {
	return
}

func (n noopCache) Set(context.Context, string, interface{}) error {
	return nil
}

func BenchmarkLookup(b *testing.B) {
	b.Run("Hit", func(b *testing.B) {
		c := lookup.NewCache(mapCache{"hit": "hit"}, func(ctx context.Context, key string) (val interface{}, err error) {
			return nil, nil
		}, nil)

		for i := 0; i < b.N; i++ {
			c.Get(context.Background(), "hit")
		}
	})

	b.Run("Miss", func(b *testing.B) {
		c := lookup.NewCache(noopCache{}, func(ctx context.Context, key string) (val interface{}, err error) {
			return nil, nil
		}, nil)

		for i := 0; i < b.N; i++ {
			c.Get(context.Background(), "miss")
		}
	})
}
