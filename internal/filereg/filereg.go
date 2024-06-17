package filereg

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/cespare/xxhash/v2"
)

// Registry keeps track of given files with their checksum
type Registry struct {
	lock           sync.Mutex
	hasher         *xxhash.Digest
	checksumByPath map[string]uint64
}

func NewRegistry() *Registry {
	return &Registry{
		hasher:         xxhash.New(),
		checksumByPath: make(map[string]uint64),
	}
}

// Register returns (true,nil) if filePath existed before and had a
// different checksum, otherwise returns (false,nil).
func (r *Registry) Register(filePath string) (updated bool, err error) {
	file, err := os.Open(filePath)
	if err != nil {
		return false, fmt.Errorf("opening file: %w", err)
	}
	defer file.Close()

	r.lock.Lock()
	defer r.lock.Unlock()

	r.hasher.Reset()
	if _, err := io.Copy(r.hasher, file); err != nil {
		return false, fmt.Errorf("copying to xxhash: %w", err)
	}
	checksum := r.hasher.Sum64()

	currentChecksum, ok := r.checksumByPath[filePath]
	if !ok {
		r.checksumByPath[filePath] = checksum
		return false, nil
	}
	if updated = currentChecksum != checksum; updated {
		r.checksumByPath[filePath] = checksum
	}
	return updated, nil
}

func (r *Registry) Deregister(filePath string) {
	r.lock.Lock()
	defer r.lock.Unlock()
	delete(r.checksumByPath, filePath)
}

func (r *Registry) Get(filePath string) (checksum uint64, ok bool) {
	r.lock.Lock()
	defer r.lock.Unlock()
	s, ok := r.checksumByPath[filePath]
	return s, ok
}
