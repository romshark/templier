// Package action provides a simple helper for consolidating custom watcher actions.
package action

import "sync"

// Type is an action type.
type Type int8

const (
	// ActionNone requires no rebuild, no restart, no reload.
	ActionNone Type = iota

	// ActionReload requires browser tab reload.
	ActionReload

	// ActionRestart requires restarting the server.
	ActionRestart

	// ActionRebuild requires rebuilding and restarting the server.
	ActionRebuild
)

// SyncStatus action status for concurrent use.
type SyncStatus struct {
	lock   sync.Mutex
	status Type
}

// Require sets the requirement status to t, if current requirement is a subset.
func (s *SyncStatus) Require(t Type) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if t > s.status {
		s.status = t
	}
}

// Load returns the current requirement status.
func (s *SyncStatus) Load() Type {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.status
}
