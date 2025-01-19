package ctxrun_test

import (
	"context"
	"testing"

	"github.com/romshark/templier/internal/ctxrun"
	"github.com/stretchr/testify/require"
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

	require.NoError(t, <-ctxErrBefore)

	ctxErrBefore2 := make(chan error, 1)
	r.Go(context.Background(), func(ctx context.Context) {
		ctxErrBefore2 <- ctx.Err()
	})

	// Unblock first goroutine to read its context error.
	blockFirstGoroutine <- struct{}{}

	require.NoError(t, <-ctxErrBefore2)
	require.Equal(t, context.Canceled, <-ctxErrAfter)
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

	require.Equal(t, 42, <-ctxValue)
}
