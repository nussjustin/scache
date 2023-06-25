package scache

import (
	"context"

	"go4.org/mem"
)

type chainedCache[T any] struct {
	cs []Cache[T]
}

var _ Cache[any] = &chainedCache[any]{}

// NewChainedCache returns a Cache that tries to retrieve values from multiple caches in
// order and adds new values to multiple caches at once.
func NewChainedCache[T any](cs ...Cache[T]) Cache[T] {
	css := make([]Cache[T], len(cs))
	copy(css, cs)
	return &chainedCache[T]{cs: css}
}

// Get implements the Cache interface.
func (cc *chainedCache[T]) Get(ctx context.Context, key mem.RO) (entry EntryView[T], ok bool) {
	for _, c := range cc.cs {
		entry, ok = c.Get(ctx, key)
		if ok {
			return
		}
	}
	return EntryView[T]{}, false
}

// GetMany returns multiple entries at once. See the global [GetMany] function for more details.
func (cc *chainedCache[T]) GetMany(ctx context.Context, keys ...mem.RO) []EntryView[T] {
	if len(cc.cs) == 0 {
		return make([]EntryView[T], len(keys))
	}

	entries := GetMany[T](ctx, cc.cs[0], keys...)

	var missing int
	for i := range entries {
		if entries[i].CreatedAt.IsZero() {
			missing++
		}
	}

	for i := 1; i < len(cc.cs) && missing != 0; i++ {
		entries2 := GetMany[T](ctx, cc.cs[i], keys...)

		for j := range entries2 {
			if entries[j].CreatedAt.IsZero() && !entries2[j].CreatedAt.IsZero() {
				entries[j] = entries2[j]
				missing--
			}
		}
	}

	return entries
}

func (cc *chainedCache[T]) Set(ctx context.Context, key mem.RO, entry Entry[T]) error {
	for _, c := range cc.cs {
		if err := c.Set(ctx, key, entry); err != nil {
			return err
		}
	}
	return nil
}
