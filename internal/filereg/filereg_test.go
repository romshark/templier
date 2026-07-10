package filereg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/romshark/templier/internal/filereg"

	"github.com/alecthomas/assert/v2"
)

func TestRegistry(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	pathFoo := filepath.Join(base, "foo")
	pathBar := filepath.Join(base, "bar")

	err := os.WriteFile(pathFoo, []byte("foo1"), 0o644)
	assert.NoError(t, err)
	err = os.WriteFile(pathBar, []byte("bar1"), 0o644)
	assert.NoError(t, err)

	r := filereg.New()

	{ // Make sure foo doesn't exist.
		checksum, ok := r.Get(pathFoo)
		assert.False(t, ok)
		assert.Zero(t, checksum)
	}
	{ // Make sure bar doesn't exist.
		checksum, ok := r.Get(pathBar)
		assert.False(t, ok)
		assert.Zero(t, checksum)
	}

	{ // Register foo.
		updated, err := r.Add(pathFoo)
		assert.True(t, updated)
		assert.NoError(t, err)
	}
	{ // Register bar.
		updated, err := r.Add(pathBar)
		assert.True(t, updated)
		assert.NoError(t, err)
	}
	{ // Re-register bar, expect no update
		updated, err := r.Add(pathBar)
		assert.False(t, updated)
		assert.NoError(t, err)
	}

	{ // Make sure foo & bar exist and have different checksums.
		checksumFoo, ok := r.Get(pathFoo)
		assert.True(t, ok)
		assert.NotZero(t, checksumFoo)

		checksumBar, ok := r.Get(pathBar)
		assert.True(t, ok)
		assert.NotZero(t, checksumBar)

		assert.NotEqual(t, checksumFoo, checksumBar)
	}

	{ // Change foo and expect it to be updated when re-registering.
		err := os.WriteFile(pathFoo, []byte("foo2"), 0o644)
		assert.NoError(t, err)
		updated, err := r.Add(pathFoo)
		assert.NoError(t, err)
		assert.True(t, updated)
	}

	// Remove both foo & bar and make sure they don't exist anymore.
	r.Remove(pathFoo)
	r.Remove(pathBar)
	{
		checksum, ok := r.Get(pathFoo)
		assert.False(t, ok)
		assert.Zero(t, checksum)
	}
	{
		checksum, ok := r.Get(pathBar)
		assert.False(t, ok)
		assert.Zero(t, checksum)
	}
}

func TestRegistryAddErrFileNotFound(t *testing.T) {
	t.Parallel()

	r := filereg.New()
	updated, err := r.Add("non-existent_file")
	assert.False(t, updated)
	assert.IsError(t, err, os.ErrNotExist)
}

func TestRegistryReset(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	p := filepath.Join(base, "foo")

	err := os.WriteFile(p, []byte("foo"), 0o644)
	assert.NoError(t, err)

	r := filereg.New()

	assert.Equal(t, 0, r.Len())

	updated, err := r.Add(p)
	assert.True(t, updated)
	assert.NoError(t, err)

	assert.Equal(t, 1, r.Len())

	r.Reset()

	assert.Equal(t, 0, r.Len())

	checksum, ok := r.Get(p)
	assert.False(t, ok)
	assert.Zero(t, checksum)
}

func TestRegistryRemoveWithPrefix(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	pathFoo := filepath.Join(base, "foo")
	pathBar := filepath.Join(base, "bar")

	err := os.WriteFile(pathFoo, []byte("foo"), 0o644)
	assert.NoError(t, err)

	err = os.WriteFile(pathBar, []byte("bar"), 0o644)
	assert.NoError(t, err)

	r := filereg.New()

	assert.Equal(t, 0, r.Len())

	updated, err := r.Add(pathFoo)
	assert.True(t, updated)
	assert.NoError(t, err)

	updated, err = r.Add(pathBar)
	assert.True(t, updated)
	assert.NoError(t, err)

	assert.Equal(t, 2, r.Len())

	r.RemoveWithPrefix(base)

	assert.Equal(t, 0, r.Len())

	{
		checksum, ok := r.Get(pathFoo)
		assert.False(t, ok)
		assert.Zero(t, checksum)
	}
	{
		checksum, ok := r.Get(pathBar)
		assert.False(t, ok)
		assert.Zero(t, checksum)
	}
}
