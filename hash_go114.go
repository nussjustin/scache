// +build go1.14

package scache

import (
	"hash/maphash"
)

type hasher struct {
	seed maphash.Seed
}

func newHasher() hasher {
	return hasher{
		seed: maphash.MakeSeed(),
	}
}

func (h hasher) hash(s string) uint64 {
	var sh maphash.Hash
	sh.SetSeed(h.seed)
	sh.WriteString(s)
	return sh.Sum64()
}
