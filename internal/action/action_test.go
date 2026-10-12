package action_test

import (
	"testing"

	"github.com/romshark/templier/internal/action"

	"github.com/alecthomas/assert/v2"
)

func TestRequire(t *testing.T) {
	t.Parallel()

	var s action.SyncStatus
	assert.Equal(t, action.ActionNone, s.Load())

	s.Require(action.ActionReload)
	assert.Equal(t, action.ActionReload, s.Load(), "overwrite")

	s.Require(action.ActionRestart)
	assert.Equal(t, action.ActionRestart, s.Load(), "overwrite")

	s.Require(action.ActionReload)
	assert.Equal(t, action.ActionRestart, s.Load(), "no overwrite")

	s.Require(action.ActionRebuild)
	assert.Equal(t, action.ActionRebuild, s.Load(), "overwrite")
}
