package main

import "sync"

type SyncStringSet struct {
	lock sync.Mutex
	m    map[string]struct{}
}

func NewSyncStringSet() *SyncStringSet { return &SyncStringSet{m: map[string]struct{}{}} }

func (s *SyncStringSet) Len() int {
	s.lock.Lock()
	defer s.lock.Unlock()
	return len(s.m)
}

func (s *SyncStringSet) Store(key string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.m[key] = struct{}{}
}

func (s *SyncStringSet) Delete(key string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	delete(s.m, key)
}

func (s *SyncStringSet) ForEach(fn func(key string)) {
	s.lock.Lock()
	defer s.lock.Unlock()
	for k := range s.m {
		fn(k)
	}
}
