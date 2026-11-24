package ctxrun_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romshark/templier/internal/ctxrun"
	"github.com/alecthomas/assert/v2"
)

func TestRunner(t *testing.T) {
	r := ctxrun.New()

	blockFirstGoroutine := make(chan struct{})
	ctxErrBefore := make(chan error, 1)
	ctxErrAfter := make(chan error)
	r.Go(context.Background(), func(ctx context.Context) {
		// Will write to buffer and immediately continue.
		ctxErrBefore <- ctx.Err()
		// Will block until second call to Go and manual unblock
		<-blockFirstGoroutine
		ctxErrAfter <- ctx.Err()
	})

	assert.NoError(t, <-ctxErrBefore)

	ctxErrBefore2 := make(chan error, 1)
	r.Go(context.Background(), func(ctx context.Context) {
		ctxErrBefore2 <- ctx.Err()
	})

	// Unblock first goroutine to read its context error.
	blockFirstGoroutine <- struct{}{}

	assert.NoError(t, <-ctxErrBefore2)
	assert.Equal(t, context.Canceled, <-ctxErrAfter)
}

// TestRunnerWait makes sure Wait blocks until all goroutines have returned,
// canceled ones included.
func TestRunnerWait(t *testing.T) {
	r := ctxrun.New()

	unblock := make(chan struct{})
	var returned atomic.Int32
	for range 2 {
		r.Go(context.Background(), func(ctx context.Context) {
			<-unblock
			returned.Add(1)
		})
	}

	waitReturned := make(chan struct{})
	go func() { r.Wait(); close(waitReturned) }()

	select {
	case <-waitReturned:
		t.Fatal("Wait returned while goroutines were still running")
	case <-time.After(50 * time.Millisecond):
	}

	close(unblock)
	<-waitReturned
	assert.Equal(t, int32(2), returned.Load())
}

// TestRunnerPassCtx makes sure the context passed to Go is the same
// that's received in the function fn.
func TestRunnerPassCtx(t *testing.T) {
	r := ctxrun.New()

	type ctxKey int8
	const ctxKeyValue ctxKey = 1

	ctx := context.WithValue(context.Background(), ctxKeyValue, 42)

	ctxValue := make(chan int, 1)
	r.Go(ctx, func(ctx context.Context) {
		// Will write to buffer and immediately continue.
		ctxValue <- ctx.Value(ctxKeyValue).(int)
	})

	assert.Equal(t, 42, <-ctxValue)
}
