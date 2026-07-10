package statetrack_test

import (
	"sync"
	"testing"

	"github.com/romshark/templier/internal/statetrack"

	"github.com/alecthomas/assert/v2"
)

func TestStateListener(t *testing.T) {
	t.Parallel()

	s := statetrack.NewTracker(2)

	assert.Zero(t, s.GetCustomWatcher(0))
	assert.Zero(t, s.GetCustomWatcher(1))
	assert.Zero(t, s.Get(statetrack.IndexTempl))
	assert.Zero(t, s.Get(statetrack.IndexGolangciLint))
	assert.Zero(t, s.Get(statetrack.IndexGo))
	assert.Zero(t, s.Get(statetrack.IndexExit))

	var wg sync.WaitGroup
	wg.Add(1)

	c1 := make(chan struct{}, 3)
	s.AddListener(c1)

	go func() {
		defer wg.Done()
		<-c1
		<-c1
		<-c1
	}()

	s.Set(statetrack.IndexGo, "go failed")
	s.Set(statetrack.IndexGolangciLint, "golangcilint failed")
	s.Set(statetrack.IndexTempl, "templ failed")
	s.Set(statetrack.IndexExit, "process exited with code 1")
	s.Set(statetrack.IndexOffsetCustomWatcher+1, "custom watcher failed")

	wg.Wait() // Wait for the listener goroutine to receive an update

	assert.Equal(t, "", s.GetCustomWatcher(0))
	assert.Equal(t, "custom watcher failed", s.GetCustomWatcher(1))
	assert.Equal(t, "templ failed", s.Get(statetrack.IndexTempl))
	assert.Equal(t, "golangcilint failed", s.Get(statetrack.IndexGolangciLint))
	assert.Equal(t, "go failed", s.Get(statetrack.IndexGo))
	assert.Equal(t, "process exited with code 1", s.Get(statetrack.IndexExit))
}

func TestStateReset(t *testing.T) {
	t.Parallel()

	s := statetrack.NewTracker(0)
	assert.Equal(t, -1, s.ErrIndex())

	assert.Zero(t, s.Get(statetrack.IndexTempl))
	assert.Zero(t, s.Get(statetrack.IndexGolangciLint))
	assert.Zero(t, s.Get(statetrack.IndexGo))
	assert.Zero(t, s.Get(statetrack.IndexExit))

	s.Set(statetrack.IndexGo, "go failed")
	s.Set(statetrack.IndexGolangciLint, "golangcilint failed")
	s.Set(statetrack.IndexTempl, "templ failed")
	assert.Equal(t, 0, s.ErrIndex())

	s.Reset()
	assert.Zero(t, s.Get(statetrack.IndexTempl))
	assert.Zero(t, s.Get(statetrack.IndexGolangciLint))
	assert.Zero(t, s.Get(statetrack.IndexGo))
	assert.Zero(t, s.Get(statetrack.IndexExit))

	assert.Equal(t, -1, s.ErrIndex())
}

func TestStateNoChange(t *testing.T) {
	t.Parallel()

	s := statetrack.NewTracker(0)
	assert.Equal(t, -1, s.ErrIndex())

	s.Set(statetrack.IndexGo, "go failed")
	s.Set(statetrack.IndexGolangciLint, "golangcilint failed")
	assert.Equal(t, 1, s.ErrIndex())

	c := make(chan struct{}, 3)
	s.AddListener(c)

	s.Set(statetrack.IndexGo, "go failed")
	s.Set(statetrack.IndexGolangciLint, "golangcilint failed")

	assert.Equal(t, 0, len(c))

	assert.Equal(t, "go failed", s.Get(statetrack.IndexGo))
	assert.Equal(t, "golangcilint failed", s.Get(statetrack.IndexGolangciLint))
}
