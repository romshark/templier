package debounce_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romshark/templier/internal/debounce"

	"github.com/alecthomas/assert/v2"
)

func TestDebounce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDebouncer, trigger := debounce.NewSync(time.Millisecond)
	go runDebouncer(ctx)

	var wg sync.WaitGroup
	var counter atomic.Int32

	wg.Add(1)
	trigger(func() {
		// No need to wg.Done since this will be overwritten by the second trigger
		counter.Add(1)
	})
	trigger(func() { // This cancels the first trigger
		defer wg.Done()
		counter.Add(1)
	})

	wg.Wait()
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, int32(1), counter.Load())
}

// TestTriggerWhileRunning verifies that a trigger doesn't block while fn runs,
// and that the new fn runs afterwards.
func TestTriggerWhileRunning(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDebouncer, trigger := debounce.NewSync(time.Millisecond)
	go runDebouncer(ctx)

	running, release := make(chan struct{}), make(chan struct{})
	trigger(func() {
		close(running)
		<-release
	})
	<-running

	triggered, ranNext := make(chan struct{}), make(chan struct{})
	go func() {
		trigger(func() { close(ranNext) })
		close(triggered)
	}()
	select {
	case <-triggered:
	case <-time.After(time.Second):
		t.Fatal("trigger blocked while fn was running")
	}

	close(release)
	select {
	case <-ranNext:
	case <-time.After(time.Second):
		t.Fatal("fn triggered during another's run never ran")
	}
}

func TestNoDebounce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDebouncer, trigger := debounce.NewSync(0)
	go runDebouncer(ctx)

	var counter atomic.Int32
	trigger(func() { counter.Add(1) })
	assert.Equal(t, int32(1), counter.Load())
}
