package scache_test

import (
	"testing"

	"github.com/nussjustin/scache"
)

func TestLRU(t *testing.T) {
	ctx := t.Context()

	l := scache.NewLRU[string, string](3)

	assertLen := func(want int) {
		t.Helper()
		if got := l.Len(); got != want {
			t.Errorf("got length %d, want %d", got, want)
		}
	}

	assertCacheNotContains(t, l, "missing-key")
	assertLen(0)

	_ = l.Set(ctx, "key1", "value")
	assertCacheContains(t, l, "key1", "value")
	assertLen(1)

	_ = l.Set(ctx, "key2", "value")
	assertCacheContains(t, l, "key2", "value")
	assertLen(2)

	_ = l.Set(ctx, "key3", "value")
	assertCacheContains(t, l, "key3", "value")
	assertLen(3)

	// Get first key to update the access time
	_, _, _ = l.Get(ctx, "key1")

	// The LRU size is 3, so this should remove one key
	_ = l.Set(ctx, "key4", "value")
	assertCacheContains(t, l, "key1", "value")
	assertCacheNotContains(t, l, "key2")
	assertCacheContains(t, l, "key3", "value")
	assertCacheContains(t, l, "key4", "value")
	assertLen(3)

	_ = l.Delete(ctx, "key1")
	assertCacheNotContains(t, l, "key1")
	assertLen(2)
}
