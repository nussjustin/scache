package scache

// stringSet implements a simple, efficient set of strings that avoids extra allocations for small sets.
type stringSet struct {
	fixed    [8]string
	fixedLen int
	overflow map[string]struct{}
}

func (set *stringSet) add(s string) {
	if set.contains(s) {
		return
	}
	if set.fixedLen < len(set.fixed) {
		set.fixed[set.fixedLen] = s
		set.fixedLen++
		return
	}
	if set.overflow == nil {
		set.overflow = make(map[string]struct{}, 8)
	}
	set.overflow[s] = struct{}{}
}

func (set *stringSet) all() []string {
	if set.overflow == nil {
		return set.fixed[:set.fixedLen:set.fixedLen]
	}

	s := make([]string, set.fixedLen+len(set.overflow))
	copy(s, set.fixed[:set.fixedLen])

	i := set.fixedLen
	for tag := range set.overflow {
		s[i] = tag
		i++
	}

	return s
}

func (set *stringSet) contains(s string) bool {
	for i := 0; i < set.fixedLen; i++ {
		if set.fixed[i] == s {
			return true
		}
	}
	_, ok := set.overflow[s]
	return ok
}

func (set *stringSet) empty() bool {
	return set.fixedLen == 0
}
