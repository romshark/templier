package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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
