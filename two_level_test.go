package scache_test

import (
	"context"
	"testing"

	"github.com/nussjustin/scache"
)

func TestTwoLevelBackend(t *testing.T) {
	t.Run("Get", func(t *testing.T) {
		ctx := context.Background()

		l1 := scache.NewLRU[int](4)
		l2 := scache.NewLRU[int](4)

		b := scache.NewTwoLevelBackend(l1, l2)

		assertNotContains(t, b, ctx, "key")
		assertNotContains(t, l1, ctx, "key")
		assertNotContains(t, l2, ctx, "key")

		assertNoError(t, l2.Set(ctx, "key", scache.Value(1)))

		assertContains(t, b, ctx, "key", nil, 1)
		assertContains(t, l1, ctx, "key", nil, 1)
		assertContains(t, l2, ctx, "key", nil, 1)

		assertNoError(t, l2.Set(ctx, "key", scache.Value(2)))

		assertContains(t, b, ctx, "key", nil, 1)
		assertContains(t, l1, ctx, "key", nil, 1)
		assertContains(t, l2, ctx, "key", nil, 2)
	})

	t.Run("Set", func(t *testing.T) {
		ctx := context.Background()

		l1 := scache.NewLRU[int](4)
		l2 := scache.NewLRU[int](4)

		b := scache.NewTwoLevelBackend(l1, l2)

		assertNoError(t, b.Set(ctx, "key", scache.Value(123)))

		assertNotContains(t, l1, ctx, "key")
		assertContains(t, l2, ctx, "key", nil, 123)
	})
}
