package lookup_test

import (
	"context"
	"testing"

	"go4.org/mem"

	"github.com/nussjustin/scache"
	"github.com/nussjustin/scache/lookup"
)

func BenchmarkLookup(b *testing.B) {
	b.Run("Hit", func(b *testing.B) {
		f := func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			return scache.Value(""), lookup.ErrSkip
		}

		c := lookup.NewCache[string](newMapCache[string](map[string]string{"hit": "hit"}), f, nil)

		for i := 0; i < b.N; i++ {
			c.Get(context.Background(), mem.S("hit"))
		}
	})

	b.Run("Miss", func(b *testing.B) {
		nc := scache.NewNoopCache[string]()

		f := func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			return scache.Value(""), lookup.ErrSkip
		}

		c := lookup.NewCache[string](nc, f, nil)

		for i := 0; i < b.N; i++ {
			c.Get(context.Background(), mem.S("miss"))
		}
	})
}

func BenchmarkLookupParallel(b *testing.B) {
	b.Run("Hit", func(b *testing.B) {
		f := func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			return scache.Value(""), lookup.ErrSkip
		}

		c := lookup.NewCache[string](newMapCache[string](map[string]string{"hit": "hit"}), f, nil)

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				c.Get(context.Background(), mem.S("hit"))
			}
		})
	})

	b.Run("Miss", func(b *testing.B) {
		nc := scache.NewNoopCache[string]()

		f := func(ctx context.Context, key mem.RO) (entry scache.Entry[string], err error) {
			return scache.Value(""), lookup.ErrSkip
		}

		c := lookup.NewCache[string](nc, f, nil)

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				c.Get(context.Background(), mem.S("miss"))
			}
		})
	})
}
