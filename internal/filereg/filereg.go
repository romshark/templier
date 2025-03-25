package filereg

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// Registry keeps track of given files with their checksum
type Registry struct {
	hasher         *xxhash.Digest
	checksumByPath map[string]uint64
}

func New() *Registry {
	return &Registry{
		hasher:         xxhash.New(),
		checksumByPath: make(map[string]uint64),
	}
}

// Len returns the number of registered files.
func (r *Registry) Len() int {
	return len(r.checksumByPath)
}

// Reset resets the registry removing all records.
func (r *Registry) Reset() {
	clear(r.checksumByPath)
}

// Add returns (true,nil) if filePath either wasn't added or
// existed before but had a different checksum, otherwise returns (false,nil).
func (r *Registry) Add(filePath string) (updated bool, err error) {
	file, err := os.Open(filePath)
	if err != nil {
		return false, fmt.Errorf("opening file: %w", err)
	}
	defer func() { _ = file.Close() }()

	r.hasher.Reset()
	if _, err := io.Copy(r.hasher, file); err != nil {
		return false, fmt.Errorf("copying to xxhash: %w", err)
	}
	checksum := r.hasher.Sum64()

	currentChecksum, ok := r.checksumByPath[filePath]
	if !ok {
		r.checksumByPath[filePath] = checksum
		return true, nil
	}
	if updated = currentChecksum != checksum; updated {
		r.checksumByPath[filePath] = checksum
	}
	return updated, nil
}

// Remove removes filePath from the registry, if a record exists.
func (r *Registry) Remove(filePath string) {
	delete(r.checksumByPath, filePath)
}

// RemoveWithPrefix removes all records with the given prefix.
func (r *Registry) RemoveWithPrefix(prefix string) {
	for p := range r.checksumByPath {
		if strings.HasPrefix(p, prefix) {
			delete(r.checksumByPath, p)
		}
	}
}

// Get returns (checksum,true) if a file record exists for filePath,
// otherwise returns (0,false).
func (r *Registry) Get(filePath string) (checksum uint64, ok bool) {
	s, ok := r.checksumByPath[filePath]
	return s, ok
}
