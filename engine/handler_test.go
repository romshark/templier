package engine

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romshark/templier/internal/action"
	"github.com/romshark/templier/internal/broadcaster"
	"github.com/romshark/templier/internal/ctxrun"
	"github.com/romshark/templier/internal/statetrack"
	"github.com/romshark/templier/internal/watcher"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
)

// TestCustomWatcherOnExcludedDir verifies that a custom watcher
// with include patterns matching an excluded directory still triggers
// when a file in that directory changes.
// For example, a user may exclude "static/**" to avoid rebuilding
// the server but still want a custom watcher to reload the browser
// when a CSS file in "static/" changes.
func TestCustomWatcherOnExcludedDir(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(base, "static"), 0o777))

	var handlerCalled atomic.Bool

	reload := broadcaster.NewSignalBroadcaster()
	st := statetrack.NewTracker(1)

	engine := &Engine{
		conf: Config{
			App: AppConfig{
				Exclude: []string{"static/**"},
			},
		},
		logger:         slog.Default(),
		chRerunServer:  make(chan struct{}, 1),
		chRunNewServer: make(chan string, 1),
	}

	handler := &fileChangeHandler{
		engine:         engine,
		watchBasePath:  base,
		stateTracker:   st,
		reload:         reload,
		debounced:      func(fn func()) { fn() },
		buildRunner:    ctxrun.New(),
		customWatchers: []customWatcher{
			{
				name:    "css-reload",
				include: []string{"static/**/*.css"},
				// No cmd — just require reload.
				debounced: func(fn func()) { fn() },
				requires:  action.ActionReload,
			},
		},
	}

	// Set up the watcher the same way the engine does:
	// use handler.handle as onChange, then apply app.Exclude via Ignore.
	fsWatcher, err := watcher.New(base, slog.Default(),
		func(ctx context.Context, e fsnotify.Event) error {
			handlerCalled.Store(true)
			return handler.handle(ctx, e)
		},
	)
	require.NoError(t, err)

	// app.Exclude patterns are no longer applied via fsWatcher.Ignore;
	// they are checked in fileChangeHandler.handle instead.

	require.NoError(t, fsWatcher.Add(base))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = fsWatcher.Run(ctx) }()
	fsWatcher.WaitRunning()

	// Listen for reload broadcasts.
	reloadCh := make(chan struct{}, 1)
	reload.AddListener(reloadCh)
	t.Cleanup(func() { reload.RemoveListener(reloadCh) })

	// Create a CSS file in the excluded directory.
	f, err := os.Create(filepath.Join(base, "static", "style.css"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// The custom watcher should trigger a reload.
	select {
	case <-reloadCh:
		// Custom watcher triggered a reload — expected behavior.
	case <-time.After(2 * time.Second):
		if !handlerCalled.Load() {
			t.Fatal("handler was never called: " +
				"the watcher's Ignore suppressed the event entirely, " +
				"preventing the custom watcher from seeing files in the excluded directory")
		}
		t.Fatal("handler was called but custom watcher did not trigger a reload")
	}
}

// TestRunAppLauncherIgnoresEmptyBinaryPath reproduces Bug B:
// a superseded build returns "" from buildServer (ctx canceled).
// Pre-fix, the launcher would set latestBinaryPath = "" and call
// exec.Command(""), logging "exec: no command" on every rerun.
// The fix drops empty paths at the chRunNewServer case so the
// launcher never enters that state.
func TestRunAppLauncherIgnoresEmptyBinaryPath(t *testing.T) {
	var logBuf bytes.Buffer
	var logMu sync.Mutex
	logger := slog.New(slog.NewTextHandler(
		&syncWriter{w: &logBuf, mu: &logMu},
		&slog.HandlerOptions{Level: slog.LevelDebug},
	))

	appHost, err := url.Parse("http://127.0.0.1:1")
	require.NoError(t, err)

	e := &Engine{
		conf: Config{
			App: AppConfig{
				DirCmd:  "./cmd/server/",
				DirWork: t.TempDir(),
				Host:    appHost,
			},
		},
		logger:         logger,
		stdout:         &bytes.Buffer{},
		stderr:         &bytes.Buffer{},
		chRerunServer:  make(chan struct{}, 1),
		chRunNewServer: make(chan string, 1),
	}

	st := statetrack.NewTracker(0)
	reload := broadcaster.NewSignalBroadcaster()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.runAppLauncher(ctx, st, reload)
		close(done)
	}()

	// Push the empty path the way a canceled build would. Before the fix
	// this would trigger exec.Command("") and log "exec: no command".
	e.chRunNewServer <- ""

	// Give the launcher a chance to process it.
	time.Sleep(150 * time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runAppLauncher did not exit after ctx cancel")
	}

	logMu.Lock()
	out := logBuf.String()
	logMu.Unlock()

	if strings.Contains(out, "exec: no command") {
		t.Fatalf("launcher tried to exec empty binary path; logs:\n%s", out)
	}
	if strings.Contains(out, "running app server") {
		t.Fatalf("launcher attempted to start app from empty path; logs:\n%s", out)
	}
	require.Contains(t, out, "ignoring empty binary path",
		"expected the empty-path guard to fire")
}

// TestRecompileDoesNotForwardEmptyPath reproduces the upstream half of
// Bug B: when a build is canceled by a newer recompile, buildServer
// returns "" and no IndexGo state is set (it's a cancel, not a failure).
// The recompile callback must NOT forward that empty path to
// chRunNewServer — otherwise the launcher would exec("").
//
// We trigger the cancellation by causing the build context to be
// canceled mid-flight via the buildRunner: a second recompile call
// cancels the first. With a build that takes long enough for the
// second call to land first, the first build returns "" through the
// canceled path. We then assert nothing empty arrives on
// chRunNewServer.
func TestRecompileDoesNotForwardEmptyPath(t *testing.T) {
	dir := t.TempDir()
	cmdDir := filepath.Join(dir, "cmd", "server")
	require.NoError(t, os.MkdirAll(cmdDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module testapp\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cmdDir, "main.go"),
		[]byte("package main\nfunc main(){}\n"), 0o644))

	// buildServer invokes `go build` with workDir="" so DirCmd resolves
	// against the process cwd. Chdir into the synthetic project for the
	// duration of the test.
	origCwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	appHost, perr := url.Parse("http://127.0.0.1:1")
	require.NoError(t, perr)

	e := &Engine{
		conf: Config{
			App: AppConfig{
				DirCmd:     "./cmd/server/",
				DirSrcRoot: dir,
				DirWork:    dir,
				Host:       appHost,
			},
		},
		logger: slog.New(slog.DiscardHandler),
		stdout:         &bytes.Buffer{},
		stderr:         &bytes.Buffer{},
		chRerunServer:  make(chan struct{}, 1),
		chRunNewServer: make(chan string, 16),
	}

	st := statetrack.NewTracker(0)
	reload := broadcaster.NewSignalBroadcaster()

	h := &fileChangeHandler{
		engine:            e,
		binaryOutBasePath: t.TempDir(),
		watchBasePath:     dir,
		stateTracker:      st,
		reload:            reload,
		debounced:         func(fn func()) { fn() },
		buildRunner:       ctxrun.New(),
	}

	ctx := context.Background()

	// Kick off many recompiles back-to-back so most of them get their
	// contexts canceled by the next. The first N-1 should return ""
	// from buildServer via the ctx-canceled path; only the last build
	// (whichever wins) is allowed to forward a path.
	const iterations = 6
	for i := 0; i < iterations; i++ {
		h.recompile(ctx, fsnotify.Event{Name: "x.go"})
	}

	// Drain chRunNewServer for a window long enough to outlast any
	// in-flight build of the tiny test app, plus a grace period for
	// any (buggy) empty sends from canceled builds.
	deadline := time.After(15 * time.Second)
	var paths []string
loop:
	for {
		select {
		case p := <-e.chRunNewServer:
			paths = append(paths, p)
			if p != "" {
				// Give a short tail to catch any straggling empty
				// sends that would have followed a real build.
				select {
				case extra := <-e.chRunNewServer:
					paths = append(paths, extra)
				case <-time.After(500 * time.Millisecond):
				}
				break loop
			}
		case <-deadline:
			break loop
		}
	}

	for i, p := range paths {
		if p == "" {
			t.Fatalf("recompile forwarded empty path at index %d "+
				"(all paths: %v)", i, paths)
		}
	}
	require.NotEmpty(t, paths,
		"expected at least one successful build to forward a non-empty path")
}

type syncWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
