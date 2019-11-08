// +build !go1.14

package sharding

import "hash/fnv"

type Hasher struct{}

func NewHasher() Hasher {
	return Hasher{}
}

func (h Hasher) Hasher(s string) uint64 {
	f := fnv.New32()
	_, _ = f.Write([]byte(s))
	return uint64(f.Sum32())
}
