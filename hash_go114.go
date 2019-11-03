// +build go1.14

package scache

import (
	"hash/maphash"
	"math/rand"
)

type hasher struct {
	seed maphash.Seed
}

func newHasher() hasher {
	return hasher{
		seed: maphash.MakeSeed(rand.Uint64()),
	}
}

func (h hasher) hash(s string) uint64 {
	var sh maphash.Hash
	sh.SetSeed(h.seed)
	sh.AddString(s)
	return sh.Sum64()
}
