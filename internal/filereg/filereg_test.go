package filereg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/romshark/templier/internal/filereg"
	"github.com/stretchr/testify/require"
)

func TestRegistry(t *testing.T) {
	base := t.TempDir()
	pathFoo := filepath.Join(base, "foo")
	pathBar := filepath.Join(base, "bar")

	err := os.WriteFile(pathFoo, []byte("foo1"), 0o644)
	require.NoError(t, err)
	err = os.WriteFile(pathBar, []byte("bar1"), 0o644)
	require.NoError(t, err)

	r := filereg.NewRegistry()

	{ // Make sure foo doesn't exist.
		checksum, ok := r.Get(pathFoo)
		require.False(t, ok)
		require.Zero(t, checksum)
	}
	{ // Make sure bar doesn't exist.
		checksum, ok := r.Get(pathBar)
		require.False(t, ok)
		require.Zero(t, checksum)
	}

	{ // Register foo.
		updated, err := r.Register(pathFoo)
		require.False(t, updated)
		require.NoError(t, err)
	}
	{ // Register bar.
		updated, err := r.Register(pathBar)
		require.False(t, updated)
		require.NoError(t, err)
	}
	{ // Re-register bar, expect no update
		updated, err := r.Register(pathBar)
		require.False(t, updated)
		require.NoError(t, err)
	}

	{ // Make sure foo & bar exist and have different checksums.
		checksumFoo, ok := r.Get(pathFoo)
		require.True(t, ok)
		require.NotZero(t, checksumFoo)

		checksumBar, ok := r.Get(pathBar)
		require.True(t, ok)
		require.NotZero(t, checksumBar)

		require.NotEqual(t, checksumFoo, checksumBar)
	}

	{ // Change foo and expect it to be updated when re-registering.
		err := os.WriteFile(pathFoo, []byte("foo2"), 0o644)
		require.NoError(t, err)
		updated, err := r.Register(pathFoo)
		require.NoError(t, err)
		require.True(t, updated)
	}

	// Deregister both foo & bar and make sure they don't exist anymore.
	r.Deregister(pathFoo)
	r.Deregister(pathBar)
	{
		checksum, ok := r.Get(pathFoo)
		require.False(t, ok)
		require.Zero(t, checksum)
	}
	{
		checksum, ok := r.Get(pathBar)
		require.False(t, ok)
		require.Zero(t, checksum)
	}
}

func TestRegistryRegisterErrFileNotFound(t *testing.T) {
	r := filereg.NewRegistry()
	updated, err := r.Register("non-existent_file")
	require.False(t, updated)
	require.ErrorIs(t, err, os.ErrNotExist)
}
