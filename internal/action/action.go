// Package action defines the actions custom watchers can require.
package action

// Type is an action type. Stronger actions are greater and cover weaker ones.
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
