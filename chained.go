package scache

import (
	"context"
	"time"
)

type chainedCache struct {
	cs []Cache
}

var _ Cache = (*chainedCache)(nil)

// NewChainedCache returns a Cache that tries to retrieve values from multiple caches in
// order and adds new values to multiple caches at once.
func NewChainedCache(cs ...Cache) Cache {
	css := make([]Cache, len(cs))
	copy(css, cs)
	return &chainedCache{cs: css}
}

// Get implements the Cache interface.
func (cc *chainedCache) Get(ctx context.Context, key string) (val interface{}, age time.Duration, ok bool) {
	for _, c := range cc.cs {
		val, age, ok = c.Get(ctx, key)
		if ok {
			return
		}
	}
	return nil, 0, false
}

func (cc *chainedCache) Set(ctx context.Context, key string, val interface{}) error {
	for _, c := range cc.cs {
		if err := c.Set(ctx, key, val); err != nil {
			return err
		}
	}
	return nil
}
