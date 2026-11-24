// Package ctxrun provides a context-canceling goroutine runner.
package ctxrun

import (
	"context"
	"sync"
)

func New() *Runner { return new(Runner) }

// Runner runs a goroutine and cancels the context of any previous call to run.
type Runner struct {
	lock    sync.Mutex
	counter uint64
	cancel  context.CancelFunc
	running sync.WaitGroup
}

// Go cancels the context of the currently running goroutine (if any) and runs
// fn in a new goroutine without waiting for the previous to return.
func (r *Runner) Go(ctx context.Context, fn func(ctx context.Context)) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.counter++
	id := r.counter

	// Cancel any ongoing task
	if r.cancel != nil {
		r.cancel()
	}

	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	r.running.Go(func() {
		defer func() {
			// Clear the cancel function once fn returns.
			r.lock.Lock()
			defer r.lock.Unlock()
			if r.counter == id {
				// No other run was conducted in the meanwhile.
				r.cancel = nil
			}
		}()

		fn(ctx)
	})
}

// Wait blocks until all goroutines started by Go have returned, canceled ones included.
// Like [sync.WaitGroup.Wait], it must not run concurrently with [sync.WaitGroup.Go].
func (r *Runner) Wait() { r.running.Wait() }
