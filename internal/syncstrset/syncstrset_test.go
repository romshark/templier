package syncstrset_test

import (
	"testing"

	"github.com/romshark/templier/internal/syncstrset"

	"github.com/stretchr/testify/require"
)

func TestSet(t *testing.T) {
	s := syncstrset.New()
	require.Zero(t, s.Len())

	s.ForEach(func(string) { panic("must not be called") })

	s.Delete("non-existent") // No-op

	s.Store("foo")
	s.Store("bar")
	require.Equal(t, 2, s.Len())
	{
		var res []string
		s.ForEach(func(key string) { res = append(res, key) })
		require.Contains(t, res, "foo")
		require.Contains(t, res, "bar")
		require.Len(t, res, 2)
	}

	s.Delete("foo")
	s.Delete("foo") // No-op
	require.Equal(t, 1, s.Len())
	{
		var res []string
		s.ForEach(func(key string) { res = append(res, key) })
		require.Contains(t, res, "bar")
		require.Len(t, res, 1)
	}

	s.Delete("bar")
	require.Zero(t, s.Len())
	s.ForEach(func(key string) { panic("must not be called") })
}
