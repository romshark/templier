package debounce_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romshark/templier/internal/debounce"

	"github.com/stretchr/testify/require"
)

func TestDebounce(t *testing.T) {
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
	trigger(func() { // This canceles the first trigger
		defer wg.Done()
		counter.Add(1)
	})

	wg.Wait()
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, int32(1), counter.Load())
}

func TestNoDebounce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDebouncer, trigger := debounce.NewSync(0)
	go runDebouncer(ctx)

	var counter atomic.Int32
	trigger(func() { counter.Add(1) })
	require.Equal(t, int32(1), counter.Load())
}
