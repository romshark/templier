package action_test

import (
	"testing"

	"github.com/romshark/templier/internal/action"

	"github.com/stretchr/testify/require"
)

func TestRequire(t *testing.T) {
	var s action.SyncStatus
	require.Equal(t, action.ActionNone, s.Load())

	s.Require(action.ActionReload)
	require.Equal(t, action.ActionReload, s.Load(), "overwrite")

	s.Require(action.ActionRestart)
	require.Equal(t, action.ActionRestart, s.Load(), "overwrite")

	s.Require(action.ActionReload)
	require.Equal(t, action.ActionRestart, s.Load(), "no overwrite")

	s.Require(action.ActionRebuild)
	require.Equal(t, action.ActionRebuild, s.Load(), "overwrite")
}
