package broadcaster

import "sync"

type SignalBroadcaster struct {
	lock      sync.Mutex
	listeners map[chan<- struct{}]struct{}
}

func NewSignalBroadcaster() *SignalBroadcaster {
	return &SignalBroadcaster{listeners: map[chan<- struct{}]struct{}{}}
}

// Len returns the number of listeners.
func (b *SignalBroadcaster) Len() int {
	b.lock.Lock()
	defer b.lock.Unlock()
	return len(b.listeners)
}

// AddListener adds channel c to listeners,
// which will be written to once BroadcastNonblock is invoked.
func (b *SignalBroadcaster) AddListener(c chan<- struct{}) {
	b.lock.Lock()
	defer b.lock.Unlock()
	b.listeners[c] = struct{}{}
}

// RemoveListener removes channel c from the listeners.
func (b *SignalBroadcaster) RemoveListener(c chan<- struct{}) {
	b.lock.Lock()
	defer b.lock.Unlock()
	delete(b.listeners, c)
}

// BroadcastNonblock writes to all listeners in a non-blocking manner.
// BroadcastNonblock ignores unresponsive listeners.
func (b *SignalBroadcaster) BroadcastNonblock() {
	b.lock.Lock()
	defer b.lock.Unlock()
	for l := range b.listeners {
		select {
		case l <- struct{}{}:
		default: // Ignore unresponsive listeners
		}
	}
}
