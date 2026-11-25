package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romshark/templier/internal/action"
	"github.com/romshark/templier/internal/broadcaster"
	"github.com/romshark/templier/internal/ctxrun"
	"github.com/romshark/templier/internal/debounce"
	"github.com/romshark/templier/internal/statetrack"
	"github.com/romshark/templier/internal/watcher"

	"github.com/alecthomas/assert/v2"
	"github.com/fsnotify/fsnotify"
)

// TestCustomWatcherOnExcludedDir verifies that a custom watcher
// with include patterns matching an excluded directory still triggers
// when a file in that directory changes.
// For example, a user may exclude "static/**" to avoid rebuilding
// the server but still want a custom watcher to reload the browser
// when a CSS file in "static/" changes.
func TestCustomWatcherOnExcludedDir(t *testing.T) {
	base := t.TempDir()
	assert.NoError(t, os.Mkdir(filepath.Join(base, "static"), 0o777))

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
		engine:        engine,
		watchBasePath: base,
		stateTracker:  st,
		reload:        reload,
		debounced:     func(fn func()) { fn() },
		buildRunner:   ctxrun.New(),
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
	assert.NoError(t, err)

	// app.Exclude patterns are no longer applied via fsWatcher.Ignore;
	// they are checked in fileChangeHandler.handle instead.

	assert.NoError(t, fsWatcher.Add(base))

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
	assert.NoError(t, err)
	assert.NoError(t, f.Close())

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

// TestBuildHeldBackDuringRebuildWatcher verifies that builds requested while
// a custom watcher requiring a rebuild is running are merged into that rebuild
// instead of being started only to be canceled by it.
//
// This happens when a file is saved by renaming a temporary file over it:
// the watcher ignores the temporary file but matches the saved one.
func TestBuildHeldBackDuringRebuildWatcher(t *testing.T) {
	for _, tt := range []struct {
		name       string
		appExclude []string
		include    []string
		changed    string
	}{
		{
			name:    "watched file",
			include: []string{"**/*.go"},
			changed: "app.go",
		},
		{
			// The held back build must run even though the watched file is excluded.
			name:       "watched excluded file",
			appExclude: []string{"gen/**"},
			include:    []string{"gen/**"},
			changed:    "gen/gen.go",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()

			// The build debouncer only fires when fireDebouncedBuild is called.
			var lock sync.Mutex
			var debouncedBuild func()
			fireDebouncedBuild := func() {
				t.Helper()
				lock.Lock()
				fn := debouncedBuild
				debouncedBuild = nil
				lock.Unlock()
				assert.NotZero(t, fn, "no debounced build")
				fn()
			}

			// The custom watcher runs once the function from watcherTriggered is called.
			watcherTriggered := make(chan func(), 1)

			var builds atomic.Int32
			handler := &fileChangeHandler{
				engine: &Engine{
					conf:           Config{App: AppConfig{Exclude: tt.appExclude}},
					logger:         slog.Default(),
					chRerunServer:  make(chan struct{}, 1),
					chRunNewServer: make(chan string, 1),
				},
				watchBasePath: base,
				stateTracker:  statetrack.NewTracker(1),
				reload:        broadcaster.NewSignalBroadcaster(),
				debounced: func(fn func()) {
					lock.Lock()
					defer lock.Unlock()
					debouncedBuild = fn
				},
				buildRunner: ctxrun.New(),
				build: func(ctx context.Context) string {
					builds.Add(1)
					return ""
				},
				customWatchers: []customWatcher{{
					name:      "codegen",
					include:   tt.include,
					debounced: func(fn func()) { watcherTriggered <- fn },
					requires:  action.ActionRebuild,
				}},
			}
			ctx := t.Context()

			// Rename the temporary file, which the watcher doesn't match.
			assert.NoError(t, handler.handle(ctx, fsnotify.Event{
				Name: filepath.Join(base, "sedsM7wE0"), Op: fsnotify.Rename,
			}))

			// Create the watched file, which triggers the watcher.
			assert.NoError(t, handler.handle(ctx, fsnotify.Event{
				Name: filepath.Join(base, tt.changed), Op: fsnotify.Create,
			}))
			runWatcher := <-watcherTriggered

			// The first event's debounced build fires while the watcher is busy.
			fireDebouncedBuild()
			handler.buildRunner.Wait()
			assert.Equal(t, int32(0), builds.Load(),
				"build started while the watcher was busy")

			runWatcher()

			// The debounced rebuild fires.
			fireDebouncedBuild()
			handler.buildRunner.Wait()
			assert.Equal(t, int32(1), builds.Load())
		})
	}
}

// TestBuildHeldBackUntilRebuildWatcherDone verifies that when a custom watcher
// requiring a rebuild is triggered again while running, builds stay held back
// until its last run is done.
func TestBuildHeldBackUntilRebuildWatcherDone(t *testing.T) {
	base := t.TempDir()
	runs := make(chan func(), 2)
	var builds atomic.Int32
	h := &fileChangeHandler{
		engine: &Engine{
			logger:         slog.Default(),
			chRerunServer:  make(chan struct{}, 1),
			chRunNewServer: make(chan string, 1),
		},
		watchBasePath: base,
		stateTracker:  statetrack.NewTracker(1),
		reload:        broadcaster.NewSignalBroadcaster(),
		debounced:     func(fn func()) { fn() },
		buildRunner:   ctxrun.New(),
		build: func(context.Context) string {
			builds.Add(1)
			return ""
		},
		customWatchers: []customWatcher{{
			name:      "codegen",
			include:   []string{"gen/**"},
			debounced: func(fn func()) { runs <- fn },
			requires:  action.ActionRebuild,
		}},
	}
	ctx := t.Context()
	handle := func(name string) {
		t.Helper()
		assert.NoError(t, h.handle(ctx, fsnotify.Event{
			Name: filepath.Join(base, name), Op: fsnotify.Write,
		}))
	}
	assertBuilds := func(want int32) {
		t.Helper()
		h.buildRunner.Wait()
		assert.Equal(t, want, builds.Load())
	}

	handle("gen/gen.go")
	firstRun := <-runs
	// The watcher is triggered again right as its first run starts.
	handle("gen/gen.go")
	secondRun := <-runs

	firstRun()
	// A change the watcher doesn't match.
	handle("app.go")
	assertBuilds(0)

	secondRun()
	assertBuilds(1)
}

// TestCustomWatchersTakeStrongestAction verifies that the last custom watcher
// to finish takes the strongest action the watchers require.
func TestCustomWatchersTakeStrongestAction(t *testing.T) {
	base := t.TempDir()
	reloadRuns, restartRuns := make(chan func(), 1), make(chan func(), 1)
	reload := broadcaster.NewSignalBroadcaster()
	reloads := make(chan struct{}, 1)
	reload.AddListener(reloads)
	h := &fileChangeHandler{
		engine: &Engine{
			logger:         slog.Default(),
			chRerunServer:  make(chan struct{}, 1),
			chRunNewServer: make(chan string, 1),
		},
		watchBasePath: base,
		stateTracker:  statetrack.NewTracker(2),
		reload:        reload,
		debounced:     func(fn func()) { fn() },
		buildRunner:   ctxrun.New(),
		customWatchers: []customWatcher{
			{
				name:      "css",
				include:   []string{"**/*.css"},
				debounced: func(fn func()) { reloadRuns <- fn },
				requires:  action.ActionReload,
			},
			{
				name:      "config",
				include:   []string{"**/*.toml"},
				debounced: func(fn func()) { restartRuns <- fn },
				requires:  action.ActionRestart,
			},
		},
	}
	ctx := t.Context()
	for _, name := range []string{"style.css", "config.toml"} {
		assert.NoError(t, h.handle(ctx, fsnotify.Event{
			Name: filepath.Join(base, name), Op: fsnotify.Write,
		}))
	}

	(<-reloadRuns)()
	assert.Equal(t, 0, len(reloads), "reloaded while a watcher was busy")
	assert.Equal(t, 0, len(h.engine.chRerunServer))

	(<-restartRuns)()
	assert.Equal(t, 0, len(reloads), "reloaded instead of restarting")
	assert.Equal(t, 1, len(h.engine.chRerunServer))
}

// TestHandleReturnsOnCancel verifies that handle returns once ctx is canceled
// instead of waiting forever for custom watchers or the app launcher.
func TestHandleReturnsOnCancel(t *testing.T) {
	newHandler := func(debounced func(func()), requires action.Type) *fileChangeHandler {
		base := t.TempDir()
		return &fileChangeHandler{
			engine: &Engine{
				logger:         slog.Default(),
				chRerunServer:  make(chan struct{}, 1),
				chRunNewServer: make(chan string, 1),
			},
			watchBasePath: base,
			stateTracker:  statetrack.NewTracker(1),
			reload:        broadcaster.NewSignalBroadcaster(),
			debounced:     func(fn func()) { fn() },
			buildRunner:   ctxrun.New(),
			build:         func(context.Context) string { return "" },
			customWatchers: []customWatcher{{
				name:      "watcher",
				include:   []string{"**/*.go"},
				debounced: debounced,
				requires:  requires,
			}},
		}
	}

	assertReturns := func(t *testing.T, handleErr <-chan error) {
		t.Helper()
		select {
		case err := <-handleErr:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("handle didn't return after the context was canceled")
		}
	}

	t.Run("canceled during debounce", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		// Canceling ctx makes the debouncer drop the pending watcher run.
		runDebouncer, debounced := debounce.NewSync(50 * time.Millisecond)
		go runDebouncer(ctx)
		h := newHandler(debounced, action.ActionRestart)

		handleErr := make(chan error, 1)
		go func() {
			handleErr <- h.handle(ctx, fsnotify.Event{
				Name: filepath.Join(h.watchBasePath, "app.go"), Op: fsnotify.Write,
			})
		}()
		time.Sleep(10 * time.Millisecond)
		cancel()
		assertReturns(t, handleErr)
	})

	t.Run("canceled with launcher stopped", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		h := newHandler(func(fn func()) { fn() }, action.ActionRestart)
		// A rerun is still queued, but the launcher has stopped.
		h.engine.chRerunServer <- struct{}{}

		handleErr := make(chan error, 1)
		go func() {
			handleErr <- h.handle(ctx, fsnotify.Event{
				Name: filepath.Join(h.watchBasePath, "app.go"), Op: fsnotify.Write,
			})
		}()
		assertReturns(t, handleErr)
	})
}

// TestCustomWatcherDebounceMergesEvents verifies that changes made while
// a custom watcher waits out its debounce share a single run.
func TestCustomWatcherDebounceMergesEvents(t *testing.T) {
	ctx := t.Context()
	runDebouncer, debounced := debounce.NewSync(100 * time.Millisecond)
	go runDebouncer(ctx)

	reload := broadcaster.NewSignalBroadcaster()
	reloads := make(chan struct{}, 10)
	reload.AddListener(reloads)

	base := t.TempDir()
	h := &fileChangeHandler{
		engine: &Engine{
			logger:         slog.Default(),
			chRerunServer:  make(chan struct{}, 1),
			chRunNewServer: make(chan string, 1),
		},
		watchBasePath: base,
		stateTracker:  statetrack.NewTracker(1),
		reload:        reload,
		debounced:     func(fn func()) { fn() },
		buildRunner:   ctxrun.New(),
		customWatchers: []customWatcher{{
			name:      "watcher",
			include:   []string{"**/*.go"},
			debounced: debounced,
			requires:  action.ActionReload,
		}},
	}

	// Save a file by renaming a temporary file over it.
	// The file watcher handles events one at a time.
	name := filepath.Join(base, "app.go")
	for _, op := range []fsnotify.Op{fsnotify.Remove, fsnotify.Create} {
		assert.NoError(t, h.handle(ctx, fsnotify.Event{Name: name, Op: op}))
	}

	select {
	case <-reloads:
	case <-time.After(2 * time.Second):
		t.Fatal("custom watcher didn't run")
	}
	select {
	case <-reloads:
		t.Fatal("custom watcher ran once per change")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestRecompileRemovesUnusedBinary verifies that a binary that won't be run
// is removed instead of being left on disk until templier exits.
func TestRecompileRemovesUnusedBinary(t *testing.T) {
	newHandler := func(
		st *statetrack.Tracker, build func(context.Context) string,
	) *fileChangeHandler {
		return &fileChangeHandler{
			engine: &Engine{
				logger:         slog.Default(),
				chRerunServer:  make(chan struct{}, 1),
				chRunNewServer: make(chan string, 1),
			},
			stateTracker: st,
			reload:       broadcaster.NewSignalBroadcaster(),
			debounced:    func(fn func()) { fn() },
			buildRunner:  ctxrun.New(),
			build:        build,
		}
	}
	newBinary := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "server")
		assert.NoError(t, os.WriteFile(path, nil, 0o755))
		return path
	}
	assertRemoved := func(t *testing.T, h *fileChangeHandler, path string) {
		t.Helper()
		h.buildRunner.Wait()
		assert.Equal(t, 0, len(h.engine.chRunNewServer),
			"binary sent to the app launcher")
		_, err := os.Stat(path)
		assert.IsError(t, err, os.ErrNotExist, "binary left on disk")
	}

	t.Run("superseded", func(t *testing.T) {
		// The first build compiles while linting is still running,
		// then a newer build cancels it.
		path := newBinary(t)
		compiled := make(chan struct{})
		var builds atomic.Int32
		h := newHandler(statetrack.NewTracker(0), func(ctx context.Context) string {
			if builds.Add(1) > 1 {
				return ""
			}
			close(compiled)
			<-ctx.Done()
			return path
		})
		h.recompile(t.Context(), fsnotify.Event{})
		<-compiled
		h.recompile(t.Context(), fsnotify.Event{})
		assertRemoved(t, h, path)
	})

	t.Run("error state", func(t *testing.T) {
		st := statetrack.NewTracker(1)
		st.Set(statetrack.IndexOffsetCustomWatcher, "custom watcher failed")
		path := newBinary(t)
		h := newHandler(st, func(context.Context) string { return path })
		h.recompile(t.Context(), fsnotify.Event{})
		assertRemoved(t, h, path)
	})
}

// TestBuildServerCanceledRemovesBinary verifies that a build canceled
// after go build wrote the binary doesn't leave it on disk.
func TestBuildServerCanceledRemovesBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}
	e := &Engine{
		conf:     Config{App: AppConfig{DirCmd: "./testdata/hello"}},
		logger:   slog.Default(),
		goTmpDir: t.TempDir(),
	}
	outDir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Cancel as soon as the binary is written.
	// Ignore the file go build briefly creates next to it to detect the umask.
	go func() {
		isBinary := func(p string) bool { return !strings.HasSuffix(p, "-go-tmp-umask") }
		pattern := filepath.Join(outDir, "server_*")
		for ctx.Err() == nil {
			if m, _ := filepath.Glob(pattern); slices.ContainsFunc(m, isBinary) {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	if e.buildServer(ctx, statetrack.NewTracker(0), outDir) != "" {
		t.Skip("go build returned before it was canceled")
	}
	entries, err := os.ReadDir(outDir)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(entries), "binary left on disk")
}

// TestBuildServerRemovesGoTmpDir verifies that go build's temporary files
// are removed after both completed and canceled builds.
// go build doesn't remove them itself when it's killed.
func TestBuildServerRemovesGoTmpDir(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}

	newEngine := func(compilerFlags ...string) *Engine {
		return &Engine{
			conf: Config{
				App:      AppConfig{DirCmd: "./testdata/hello"},
				Compiler: &CompilerConfig{Flags: compilerFlags},
			},
			logger:   slog.Default(),
			goTmpDir: t.TempDir(),
		}
	}

	assertDirEmpty := func(t *testing.T, dir string) {
		t.Helper()
		entries, err := os.ReadDir(dir)
		assert.NoError(t, err)
		for _, e := range entries {
			t.Errorf("leftover in go build temporary directory: %s", e.Name())
		}
	}

	t.Run("completed", func(t *testing.T) {
		e := newEngine()
		binaryPath := e.buildServer(t.Context(), statetrack.NewTracker(0), t.TempDir())
		assert.NotEqual(t, "", binaryPath)
		assertDirEmpty(t, e.goTmpDir)
	})

	t.Run("canceled", func(t *testing.T) {
		// -a rebuilds the standard library too, keeping the build busy
		// long enough to cancel it midway.
		e := newEngine("-a")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		// Cancel once go build starts writing to its work directory.
		go func() {
			pattern := filepath.Join(e.goTmpDir, "*", "go-build*", "b*")
			for ctx.Err() == nil {
				if m, _ := filepath.Glob(pattern); len(m) > 0 {
					cancel()
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()

		binaryPath := e.buildServer(ctx, statetrack.NewTracker(0), t.TempDir())
		assert.Equal(t, "", binaryPath, "build wasn't canceled")
		assertDirEmpty(t, e.goTmpDir)
	})
}

// TestResolveGoTmpDir verifies that a GOTMPDIR set by the user
// is preferred over the default directory.
func TestResolveGoTmpDir(t *testing.T) {
	// Ignore any GOTMPDIR configured on this machine.
	t.Setenv("GOENV", "off")
	t.Setenv("GOTMPDIR", "")

	t.Run("default", func(t *testing.T) {
		e := &Engine{logger: slog.Default()}
		assert.Equal(t, "default", e.resolveGoTmpDir(t.Context(), "default"))
	})

	t.Run("compiler env", func(t *testing.T) {
		userDir := t.TempDir()
		e := &Engine{
			conf: Config{Compiler: &CompilerConfig{
				Env: []string{"GOTMPDIR=" + userDir},
			}},
			logger: slog.Default(),
		}
		assert.Equal(t, userDir, e.resolveGoTmpDir(t.Context(), "default"))
	})

	t.Run("environment", func(t *testing.T) {
		userDir := t.TempDir()
		t.Setenv("GOTMPDIR", userDir)
		e := &Engine{logger: slog.Default()}
		assert.Equal(t, userDir, e.resolveGoTmpDir(t.Context(), "default"))
	})
}
