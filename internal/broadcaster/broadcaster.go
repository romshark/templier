package broadcaster

import "sync"

type SignalBroadcaster struct {
	lock      sync.Mutex
	listeners map[chan<- struct{}]struct{}
}

func NewSignalBroadcaster() *SignalBroadcaster {
	return &SignalBroadcaster{listeners: map[chan<- struct{}]struct{}{}}
}

func (b *SignalBroadcaster) AddListener(c chan<- struct{}) {
	b.lock.Lock()
	defer b.lock.Unlock()
	b.listeners[c] = struct{}{}
}

func (b *SignalBroadcaster) RemoveListener(c chan<- struct{}) {
	b.lock.Lock()
	defer b.lock.Unlock()
	delete(b.listeners, c)
}

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
