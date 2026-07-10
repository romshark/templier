package broadcaster_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/romshark/templier/internal/broadcaster"

	"github.com/alecthomas/assert/v2"
)

func TestBroadcast(t *testing.T) {
	t.Parallel()

	b := broadcaster.NewSignalBroadcaster()
	assert.Equal(t, 0, b.Len())

	var wg sync.WaitGroup
	var prepare sync.WaitGroup
	prepare.Add(2)
	var removed sync.WaitGroup
	removed.Add(2)

	var counter atomic.Int32

	wg.Go(func() {
		c := make(chan struct{}, 1)
		b.AddListener(c)
		prepare.Done()
		<-c
		counter.Add(1)
		b.RemoveListener(c)
		removed.Done()
	})

	wg.Go(func() {
		c := make(chan struct{}, 1)
		b.AddListener(c)
		prepare.Done()
		<-c
		counter.Add(1)
		b.RemoveListener(c)
		removed.Done()
	})

	prepare.Wait()
	assert.Equal(t, 2, b.Len())
	b.BroadcastNonblock()
	wg.Wait()
	assert.Equal(t, int32(2), counter.Load())

	removed.Wait()
	assert.Equal(t, 0, b.Len())
	b.BroadcastNonblock()
	assert.Equal(t, int32(2), counter.Load())
}
