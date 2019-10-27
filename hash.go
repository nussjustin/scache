// +build !go1.14

package scache

import "hash/fnv"

type hasher struct{}

func newHasher() hasher {
	return hasher{}
}

func (h hasher) hash(s string) uint64 {
	f := fnv.New32()
	_, _ = f.Write([]byte(s))
	return uint64(f.Sum32())
}
