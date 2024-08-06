package statetrack

import (
	"sync"

	"github.com/romshark/templier/internal/broadcaster"
)

const (
	IndexTempl               = 0
	IndexGolangciLint        = 1
	IndexGo                  = 2
	IndexExit                = 3
	IndexOffsetCustomWatcher = 4
)

func (t *Tracker) ErrIndex() int {
	t.lock.Lock()
	defer t.lock.Unlock()
	for i, e := range t.errMsgBuffer {
		if e != "" {
			return i
		}
	}
	return -1
}

type Tracker struct {
	// Static layout:
	// index 0: templ errors
	// index 1: golangci-lint errors
	// index 2: go compiler errors
	// index 3: process exit code != 0
	// index 4-end: custom watcher errors
	errMsgBuffer []string
	lock         sync.Mutex
	broadcaster  *broadcaster.SignalBroadcaster
}

func NewTracker(numCustomWatchers int) *Tracker {
	return &Tracker{
		errMsgBuffer: make([]string, IndexOffsetCustomWatcher+numCustomWatchers),
		broadcaster:  broadcaster.NewSignalBroadcaster(),
	}
}

// Get gets the current error message at index.
func (t *Tracker) Get(index int) string {
	t.lock.Lock()
	defer t.lock.Unlock()
	return t.errMsgBuffer[index]
}

// GetCustomWatcher gets the current error message for custom watcher at index.
func (t *Tracker) GetCustomWatcher(index int) string {
	t.lock.Lock()
	defer t.lock.Unlock()
	return t.errMsgBuffer[IndexOffsetCustomWatcher+index]
}

// NumListeners returns the number of currently active listeners.
func (s *Tracker) NumListeners() int {
	return s.broadcaster.Len()
}

// AddListener adds a listener channel.
// c will be written struct{}{} to when a state change happens.
func (s *Tracker) AddListener(c chan<- struct{}) {
	s.broadcaster.AddListener(c)
}

// RemoveListener removes a listener channel.
func (s *Tracker) RemoveListener(c chan<- struct{}) {
	s.broadcaster.RemoveListener(c)
}

// Reset resets the state and notifies all listeners.
func (t *Tracker) Reset() {
	t.lock.Lock()
	defer t.lock.Unlock()
	for i := range t.errMsgBuffer {
		t.errMsgBuffer[i] = ""
	}
	t.broadcaster.BroadcastNonblock()
}

// Set sets or resets (if msg == "") the current error message at index.
func (t *Tracker) Set(index int, msg string) {
	t.lock.Lock()
	defer t.lock.Unlock()
	if msg == t.errMsgBuffer[index] {
		return // State didn't change, ignore.
	}
	t.errMsgBuffer[index] = msg
	t.broadcaster.BroadcastNonblock()
}
