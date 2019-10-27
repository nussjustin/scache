// +build go1.14

package scache

import (
	"bytes/hash"
	"math/rand"
)

type hasher struct {
	seed hash.Seed
}

func newHasher() hasher {
	return hasher{
		seed: hash.MakeSeed(rand.Uint64()),
	}
}

func (h hasher) hash(s string) uint64 {
	var sh hash.Hash
	sh.SetSeed(h.seed)
	sh.AddString(s)
	return sh.Sum64()
}
