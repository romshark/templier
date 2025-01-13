package broadcaster_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/romshark/templier/internal/broadcaster"

	"github.com/stretchr/testify/require"
)

func TestBroadcast(t *testing.T) {
	t.Parallel()

	b := broadcaster.NewSignalBroadcaster()
	require.Equal(t, 0, b.Len())

	var wg sync.WaitGroup
	wg.Add(2)
	var prepare sync.WaitGroup
	prepare.Add(2)
	var removed sync.WaitGroup
	removed.Add(2)

	var counter atomic.Int32

	go func() {
		defer wg.Done()
		c := make(chan struct{}, 1)
		b.AddListener(c)
		prepare.Done()
		<-c
		counter.Add(1)
		b.RemoveListener(c)
		removed.Done()
	}()

	go func() {
		defer wg.Done()
		c := make(chan struct{}, 1)
		b.AddListener(c)
		prepare.Done()
		<-c
		counter.Add(1)
		b.RemoveListener(c)
		removed.Done()
	}()

	prepare.Wait()
	require.Equal(t, 2, b.Len())
	b.BroadcastNonblock()
	wg.Wait()
	require.Equal(t, int32(2), counter.Load())

	removed.Wait()
	require.Equal(t, 0, b.Len())
	b.BroadcastNonblock()
	require.Equal(t, int32(2), counter.Load())
}
