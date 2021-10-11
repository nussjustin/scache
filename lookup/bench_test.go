package lookup_test

import (
	"context"
	"testing"

	"github.com/nussjustin/scache"
	"github.com/nussjustin/scache/lookup"
	"go4.org/mem"
)

func BenchmarkLookup(b *testing.B) {
	b.Run("Hit", func(b *testing.B) {
		c := lookup.NewCache[string](newMapCache[string](map[string]string{"hit": "hit"}), func(ctx context.Context, key mem.RO) (val string, err error) {
			return "", lookup.ErrSkip
		}, nil)

		for i := 0; i < b.N; i++ {
			c.Get(context.Background(), mem.S("hit"))
		}
	})

	b.Run("Miss", func(b *testing.B) {
		// Syntax "var nc scache.Noop[string]" currently causes an error because the "[" can not be parsed
		nc := scache.Noop[string]{}

		c := lookup.NewCache[string](nc, func(ctx context.Context, key mem.RO) (val string, err error) {
			return "", lookup.ErrSkip
		}, nil)

		for i := 0; i < b.N; i++ {
			c.Get(context.Background(), mem.S("miss"))
		}
	})
}
