package syncstrset

import "sync"

// Set is a concurrency-safe string set.
type Set struct {
	lock sync.Mutex
	m    map[string]struct{}
}

func New() *Set { return &Set{m: map[string]struct{}{}} }

// Len returns the number of stored strings.
func (s *Set) Len() int {
	s.lock.Lock()
	defer s.lock.Unlock()
	return len(s.m)
}

// Store adds a string to the set.
func (s *Set) Store(key string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.m[key] = struct{}{}
}

// Delete removes a string from the set.
func (s *Set) Delete(key string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	delete(s.m, key)
}

// ForEach calls fn for every string in the store.
func (s *Set) ForEach(fn func(key string)) {
	s.lock.Lock()
	defer s.lock.Unlock()
	for k := range s.m {
		fn(k)
	}
}
