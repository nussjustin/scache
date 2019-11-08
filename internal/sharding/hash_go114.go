// +build go1.14

package sharding

import "hash/maphash"

type Hasher struct {
	seed maphash.Seed
}

func NewHasher() Hasher {
	return Hasher{
		seed: maphash.MakeSeed(),
	}
}

func (h Hasher) Hash(s string) uint64 {
	var sh maphash.Hash
	sh.SetSeed(h.seed)
	sh.WriteString(s)
	return sh.Sum64()
}
