package debounce

import (
	"context"
	"sync"
	"time"
)

// NewSync creates a new concurrency-safe debouncer.
func NewSync(duration time.Duration) (
	runDebouncer func(ctx context.Context), trigger func(fn func()),
) {
	if duration == 0 {
		// Debounce disabled, execute fn immediately.
		return func(context.Context) { /*Noop*/ }, func(fn func()) { fn() }
	}

	var lock sync.Mutex
	var fn func()
	ticker := time.NewTicker(duration)
	ticker.Stop()

	runDebouncer = func(ctx context.Context) {
		for {
			select {
			case <-ticker.C:
				lock.Lock()
				ticker.Stop()
				fn()
				fn = nil
				lock.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}
	trigger = func(fnNew func()) {
		lock.Lock()
		defer lock.Unlock()
		ticker.Reset(duration)
		fn = fnNew
	}
	return runDebouncer, trigger
}
