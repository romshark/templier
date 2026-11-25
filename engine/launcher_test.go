package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/romshark/templier/internal/broadcaster"
	"github.com/romshark/templier/internal/statetrack"

	"github.com/alecthomas/assert/v2"
)

const (
	appServerAddrEnv  = "TEMPLIER_TEST_APP_SERVER_ADDR"
	appServerCrashEnv = "TEMPLIER_TEST_APP_SERVER_CRASH_AFTER"
)

// TestAppServerHelperProcess isn't a real test. The launcher tests run it as
// the app server, which crashes after appServerCrashEnv if that's set.
func TestAppServerHelperProcess(t *testing.T) {
	addr := os.Getenv(appServerAddrEnv)
	if addr == "" {
		t.Skip("not running as app server helper process")
	}
	if d, err := time.ParseDuration(os.Getenv(appServerCrashEnv)); err == nil {
		time.AfterFunc(d, func() {
			fmt.Fprintln(os.Stderr, "panic: boom")
			os.Exit(2)
		})
	}
	// Any response passes the health check.
	fmt.Fprintln(os.Stderr, http.ListenAndServe(addr, http.NotFoundHandler()))
	os.Exit(1)
}

// startAppLauncher runs the app launcher with this test binary as the app
// server and returns once it has started. stop stops the launcher.
func startAppLauncher(t *testing.T, crashAfter string) (
	e *Engine, st *statetrack.Tracker, reloads <-chan struct{}, stop func(),
) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	addr := l.Addr().String()
	assert.NoError(t, l.Close())
	t.Setenv(appServerAddrEnv, addr)
	t.Setenv(appServerCrashEnv, crashAfter)

	exe, err := os.Executable()
	assert.NoError(t, err)
	e = &Engine{
		conf: Config{App: AppConfig{
			Host:  &url.URL{Scheme: "http", Host: addr},
			Flags: []string{"-test.run=^TestAppServerHelperProcess$"},
		}},
		logger:         slog.Default(),
		stdout:         io.Discard,
		stderr:         io.Discard,
		chRerunServer:  make(chan struct{}, 1),
		chRunNewServer: make(chan string, 1),
	}
	st = statetrack.NewTracker(0)
	reload := broadcaster.NewSignalBroadcaster()
	ch := make(chan struct{}, 10)
	reload.AddListener(ch)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.runAppLauncher(ctx, st, reload)
	}()
	stop = sync.OnceFunc(func() { cancel(); <-done })
	t.Cleanup(stop)

	// Sent only once: the launcher removes a binary once a newer one replaces it.
	e.chRunNewServer <- exe
	awaitReload(t, ch)
	return e, st, ch, stop
}

func awaitReload(t *testing.T, reloads <-chan struct{}) {
	t.Helper()
	select {
	case <-reloads:
	case <-time.After(5 * time.Second):
		t.Fatal("browser not reloaded")
	}
}

// TestAppLauncherReportsCrash verifies that an app server crashing
// after it has started is shown in the browser.
func TestAppLauncherReportsCrash(t *testing.T) {
	_, st, _, _ := startAppLauncher(t, "1s")
	deadline := time.Now().Add(5 * time.Second)
	for st.Get(statetrack.IndexExit) == "" {
		if time.Now().After(deadline) {
			t.Fatal("crash not reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Contains(t, st.Get(statetrack.IndexExit), "panic: boom")
}

// TestAppLauncherDoesntReportStops verifies that restarting
// and stopping the app server isn't reported as a crash.
func TestAppLauncherDoesntReportStops(t *testing.T) {
	e, st, reloads, stop := startAppLauncher(t, "")
	e.chRerunServer <- struct{}{}
	awaitReload(t, reloads)
	stop()
	assert.Equal(t, "", st.Get(statetrack.IndexExit))
}
