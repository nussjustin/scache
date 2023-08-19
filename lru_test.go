package scache_test

import (
	"fmt"
	"testing"

	"github.com/nussjustin/scache"
)

func TestLRU(t *testing.T) {
	ctx := testContext(t)

	const size = 8

	b := scache.NewLRU[int](size)

	assertEquals(t, b.Len(), 0)

	// Check general capacity and order
	for i := 0; i < size; i++ {
		assertNoError(t, b.Set(ctx, fmt.Sprint(i), scache.Value(i)))
	}

	assertEquals(t, b.Len(), size)

	for i := 0; i < size; i++ {
		assertContains(t, b, ctx, fmt.Sprint(i), nil, i)
	}

	assertNoError(t, b.Set(ctx, "extra", scache.Value(-1)))

	for i := 1; i < size; i++ {
		assertContains(t, b, ctx, fmt.Sprint(i), nil, i)
	}

	assertNotContains(t, b, ctx, "0")
	assertContains(t, b, ctx, "extra", nil, -1)

	assertEquals(t, b.Len(), size)

	// Check LRU logic
	assertContains(t, b, ctx, "1", nil, 1)
	assertContains(t, b, ctx, "3", nil, 3)
	assertContains(t, b, ctx, "5", nil, 5)

	assertNoError(t, b.Set(ctx, "extra1", scache.Value(-1)))
	assertNoError(t, b.Set(ctx, "extra2", scache.Value(-1)))
	assertNoError(t, b.Set(ctx, "extra3", scache.Value(-1)))

	assertContains(t, b, ctx, "1", nil, 1)
	assertContains(t, b, ctx, "3", nil, 3)
	assertContains(t, b, ctx, "5", nil, 5)

	assertNotContains(t, b, ctx, "2")
	assertNotContains(t, b, ctx, "4")
	assertNotContains(t, b, ctx, "6")

	// Check weight support
	assertNoError(t, b.Set(ctx, "weight2", scache.Item[int]{Weight: 2}))
	assertEquals(t, b.Len(), 7)

	assertNoError(t, b.Set(ctx, "weight4", scache.Item[int]{Weight: 4}))
	assertEquals(t, b.Len(), 4)

	assertNoError(t, b.Set(ctx, "weight8", scache.Item[int]{Weight: 8}))
	assertEquals(t, b.Len(), 1)

	// Weight greater than capacity
	assertNoError(t, b.Set(ctx, "weight10", scache.Item[int]{Weight: 10}))
	assertNotContains(t, b, ctx, "weight10")

	// Check deletion
	assertNoError(t, b.Set(ctx, "weight2", scache.Item[int]{Weight: 2}))
	assertNoError(t, b.Set(ctx, "weight4", scache.Item[int]{Weight: 4}))
	assertEquals(t, b.Len(), 2)

	assertNoError(t, b.Delete(ctx, "missing"))
	assertEquals(t, b.Len(), 2)

	assertContains(t, b, ctx, "weight2", nil, 0)

	assertNoError(t, b.Delete(ctx, "weight2"))
	assertEquals(t, b.Len(), 1)

	assertNotContains(t, b, ctx, "weight2")

	assertContains(t, b, ctx, "weight4", nil, 0)
}
