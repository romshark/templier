package state_test

import (
	"sync"
	"testing"

	"github.com/romshark/templier/internal/state"
	"github.com/stretchr/testify/require"
)

func TestStateListener(t *testing.T) {
	s := state.NewTracker()

	require.Equal(t, state.State{}, s.Get())

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

	s.SetErrGo("go failed")
	s.SetErrGolangCILint("golangcilint failed")
	s.SetErrTempl("templ failed")
	wg.Wait() // Wait for the listener goroutine to receive an update
	require.Equal(t, state.State{
		ErrGo:           "go failed",
		ErrGolangCILint: "golangcilint failed",
		ErrTempl:        "templ failed",
	}, s.Get())
}

func TestStateMsg(t *testing.T) {
	s := state.NewTracker()

	require.Equal(t, state.State{}, s.Get())

	s.SetErrGo("go failed")
	s.SetErrGolangCILint("golangcilint failed")
	s.SetErrTempl("templ failed")

	require.Equal(t, state.State{
		ErrGo:           "go failed",
		ErrGolangCILint: "golangcilint failed",
		ErrTempl:        "templ failed",
	}, s.Get())

	require.Equal(t, "templ failed", s.Get().Msg())
	s.SetErrTempl("")
	require.Equal(t, "golangcilint failed", s.Get().Msg())
	s.SetErrGolangCILint("")
	require.Equal(t, "go failed", s.Get().Msg())
	s.SetErrGo("")
	require.Zero(t, s.Get().Msg())
}

func TestStateReset(t *testing.T) {
	s := state.NewTracker()
	require.False(t, s.Get().IsErr())

	require.Equal(t, state.State{}, s.Get())

	s.SetErrGo("go failed")
	s.SetErrGolangCILint("golangcilint failed")
	s.SetErrTempl("templ failed")
	require.True(t, s.Get().IsErr())

	s.Reset()
	require.Equal(t, state.State{}, s.Get())
	require.False(t, s.Get().IsErr())
}
