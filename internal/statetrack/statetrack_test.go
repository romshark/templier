package statetrack_test

import (
	"sync"
	"testing"

	"github.com/romshark/templier/internal/statetrack"
	"github.com/stretchr/testify/require"
)

func TestStateListener(t *testing.T) {
	s := statetrack.NewTracker(2)

	require.Zero(t, s.GetCustomWatcher(0))
	require.Zero(t, s.GetCustomWatcher(1))
	require.Zero(t, s.Get(statetrack.IndexTempl))
	require.Zero(t, s.Get(statetrack.IndexGolangciLint))
	require.Zero(t, s.Get(statetrack.IndexGo))
	require.Zero(t, s.Get(statetrack.IndexExit))

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

	require.Equal(t, "", s.GetCustomWatcher(0))
	require.Equal(t, "custom watcher failed", s.GetCustomWatcher(1))
	require.Equal(t, "templ failed", s.Get(statetrack.IndexTempl))
	require.Equal(t, "golangcilint failed", s.Get(statetrack.IndexGolangciLint))
	require.Equal(t, "go failed", s.Get(statetrack.IndexGo))
	require.Equal(t, "process exited with code 1", s.Get(statetrack.IndexExit))
}

func TestStateReset(t *testing.T) {
	s := statetrack.NewTracker(0)
	require.Equal(t, -1, s.ErrIndex())

	require.Zero(t, s.Get(statetrack.IndexTempl))
	require.Zero(t, s.Get(statetrack.IndexGolangciLint))
	require.Zero(t, s.Get(statetrack.IndexGo))
	require.Zero(t, s.Get(statetrack.IndexExit))

	s.Set(statetrack.IndexGo, "go failed")
	s.Set(statetrack.IndexGolangciLint, "golangcilint failed")
	s.Set(statetrack.IndexTempl, "templ failed")
	require.Equal(t, 0, s.ErrIndex())

	s.Reset()
	require.Zero(t, s.Get(statetrack.IndexTempl))
	require.Zero(t, s.Get(statetrack.IndexGolangciLint))
	require.Zero(t, s.Get(statetrack.IndexGo))
	require.Zero(t, s.Get(statetrack.IndexExit))

	require.Equal(t, -1, s.ErrIndex())
}

func TestStateNoChange(t *testing.T) {
	s := statetrack.NewTracker(0)
	require.Equal(t, -1, s.ErrIndex())

	s.Set(statetrack.IndexGo, "go failed")
	s.Set(statetrack.IndexGolangciLint, "golangcilint failed")
	require.Equal(t, 1, s.ErrIndex())

	c := make(chan struct{}, 3)
	s.AddListener(c)

	s.Set(statetrack.IndexGo, "go failed")
	s.Set(statetrack.IndexGolangciLint, "golangcilint failed")

	require.Len(t, c, 0)

	require.Equal(t, "go failed", s.Get(statetrack.IndexGo))
	require.Equal(t, "golangcilint failed", s.Get(statetrack.IndexGolangciLint))
}
