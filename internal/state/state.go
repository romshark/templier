package state

import (
	"sync"

	"github.com/romshark/templier/internal/broadcaster"
)

type State struct {
	ErrTempl        string
	ErrGolangCILint string
	ErrGo           string
}

func (s State) Msg() string {
	if s.ErrTempl != "" {
		// Code and lint may be OK but if templ is broken then we're not OK.
		return s.ErrTempl
	}
	if s.ErrGolangCILint != "" {
		// Code may compile, but if golangci-lint failed then we're not OK.
		return s.ErrGolangCILint
	}
	if s.ErrGo != "" {
		return s.ErrGo
	}
	return ""
}

func (s State) IsErr() bool {
	return s.ErrTempl != "" || s.ErrGolangCILint != "" || s.ErrGo != ""
}

type Tracker struct {
	state       State
	lock        sync.Mutex
	broadcaster *broadcaster.SignalBroadcaster
}

func NewTracker() *Tracker {
	return &Tracker{
		broadcaster: broadcaster.NewSignalBroadcaster(),
	}
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
func (s *Tracker) Reset() {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.state.ErrTempl = ""
	s.state.ErrGolangCILint = ""
	s.state.ErrGo = ""
	s.broadcaster.BroadcastNonblock()
}

// SetErrTempl sets or resets (if "") the current templ error
// and notifies all listeners.
func (s *Tracker) SetErrTempl(msg string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if msg == s.state.ErrTempl {
		return // State didn't change, ignore.
	}
	s.state.ErrTempl = msg
	s.broadcaster.BroadcastNonblock()
}

// SetErrGolangCILint sets or resets (if "") the current golangci-lint error
// and notifies all listeners.
func (s *Tracker) SetErrGolangCILint(msg string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if msg == s.state.ErrGolangCILint {
		return // State didn't change, ignore.
	}
	s.state.ErrGolangCILint = msg
	s.broadcaster.BroadcastNonblock()
}

// SetErrGo sets or resets (if "") the current Go error
// and notifies all listeners.
func (s *Tracker) SetErrGo(msg string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if msg == s.state.ErrGo {
		return // State didn't change, ignore.
	}
	s.state.ErrGo = msg
	s.broadcaster.BroadcastNonblock()
}

// Get returns the current state.
func (s *Tracker) Get() State {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.state
}
