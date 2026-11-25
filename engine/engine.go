package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/romshark/templier/internal/action"
	"github.com/romshark/templier/internal/broadcaster"
	"github.com/romshark/templier/internal/cmdrun"
	"github.com/romshark/templier/internal/ctxrun"
	"github.com/romshark/templier/internal/debounce"
	"github.com/romshark/templier/internal/server"
	"github.com/romshark/templier/internal/statetrack"
	"github.com/romshark/templier/internal/watcher"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sync/errgroup"
)

const serverHealthPreflightWaitInterval = 100 * time.Millisecond

// serverShutdownTimeout defines how long to wait for the app server
// process to exit after it was asked to stop before killing it.
const serverShutdownTimeout = 5 * time.Second

// waitTimeout waits for wg and reports whether it finished within d.
func waitTimeout(wg *sync.WaitGroup, d time.Duration) (finished bool) {
	done := make(chan struct{})
	go func() { defer close(done); wg.Wait() }()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// Engine is the core templier engine.
// Use [New] to create an Engine and [Engine.Run] to start it.
type Engine struct {
	conf        Config
	version     string
	logger      *slog.Logger
	onClearLogs func()
	stdout      io.Writer
	stderr      io.Writer

	// Runtime coordination state.
	rerunActive    atomic.Bool
	chRerunServer  chan struct{}
	chRunNewServer chan string

	// goTmpDir is where each go build gets its own GOTMPDIR.
	// Empty means the OS temp directory.
	goTmpDir string
}

// Options configures an [Engine].
type Options struct {
	// Logger sets the logger for the engine.
	// If nil, [slog.Default] is used.
	Logger *slog.Logger

	// ClearLogs is called when the engine wants to clear the log output
	// (e.g. on server restart or file change, depending on [LogConfig.ClearOn]).
	// If nil, log clearing is a no-op.
	ClearLogs func()

	// Stdout is the writer for the app server's stdout.
	// If nil, [os.Stdout] is used.
	Stdout io.Writer

	// Stderr is the writer for the app server's stderr.
	// If nil, [os.Stderr] is used.
	Stderr io.Writer

	// Version is the version string (e.g. "1.2.3").
	// When using the CLI, this is set via goreleaser ldflags.
	// When empty, [Version] is used as fallback.
	Version string

	// Commit is the VCS commit hash. Set via goreleaser ldflags in the CLI.
	Commit string

	// Date is the VCS commit date. Set via goreleaser ldflags in the CLI.
	Date string
}

// New creates a new [Engine] with the given configuration.
// It validates the configuration and checks that required external
// tools (templ, optionally golangci-lint) are available.
func New(conf Config, opts Options) (*Engine, error) {
	conf.applyDefaults()
	if err := conf.Validate(); err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	conf.checkCustomWatcherCmds(logger)

	e := &Engine{
		conf:           conf,
		version:        opts.Version,
		logger:         logger,
		onClearLogs:    opts.ClearLogs,
		stdout:         stdout,
		stderr:         stderr,
		chRerunServer:  make(chan struct{}, 1),
		chRunNewServer: make(chan string, 1),
	}
	return e, nil
}

// Run starts the templier engine. It blocks until ctx is canceled
// or a fatal error occurs. The engine will:
//  1. Start templ in watch mode.
//  2. Start the templier reverse proxy server.
//  3. Start the app launcher (builds, runs, and manages the app server process).
//  4. Watch for file changes and trigger rebuilds/restarts as needed.
func (e *Engine) Run(ctx context.Context) error {
	if err := e.checkTemplVersion(ctx); err != nil {
		return fmt.Errorf("checking templ version: %w", err)
	}

	// Create a temporary directory for compiled server binaries.
	tempDirPath, err := os.MkdirTemp("", "templier-*")
	if err != nil {
		return fmt.Errorf("creating temporary directory: %w", err)
	}
	e.logger.Debug("set server binaries output path", "path", tempDirPath)
	defer func() {
		e.logger.Debug("removing server binaries output directory", "path", tempDirPath)
		if err := os.RemoveAll(tempDirPath); err != nil {
			e.logger.Error("deleting temporary directory", "err", err)
		}
	}()

	e.goTmpDir = e.resolveGoTmpDir(ctx, tempDirPath)
	e.logger.Debug("set go build temporary files path", "path", e.goTmpDir)

	reload := broadcaster.NewSignalBroadcaster()
	st := statetrack.NewTracker(len(e.conf.CustomWatchers))

	errgrp, ctx := errgroup.WithContext(ctx)
	var wg sync.WaitGroup

	// A buffer of 64 allows up to 64 change signals to pile up
	// before the notifier starts dropping updates.
	templChange := make(chan cmdrun.TemplChange, 64)

	wg.Add(1)
	errgrp.Go(func() error {
		defer wg.Done()
		// Run templ in watch mode to create debug components.
		err := cmdrun.RunTemplWatch(
			ctx, e.conf.App.DirSrcRoot, e.conf.TemplCmd, e.logger, st, templChange,
		)
		if err != nil && !errors.Is(err, context.Canceled) {
			err = fmt.Errorf("running templ generate watch mode: %w", err)
			e.logger.Error(err.Error())
		}
		e.logger.Debug("templ generate watch stopped")
		return err
	})

	// Initialize custom watchers.
	customWatchers := make([]customWatcher, len(e.conf.CustomWatchers))
	for i, w := range e.conf.CustomWatchers {
		debouncer, debounced := debounce.NewSync(w.Debounce)
		// Each watcher runs on its debouncer, so Run waits for it too.
		wg.Go(func() { debouncer(ctx) })

		customWatchers[i] = customWatcher{
			name:      w.Name,
			debounced: debounced,
			cmd:       w.Cmd,
			failOnErr: w.FailOnErr,
			include:   w.Include,
			exclude:   w.Exclude,
			requires:  action.Type(w.Requires),
		}
	}

	wg.Add(1)
	errgrp.Go(func() error {
		defer wg.Done()
		err := e.runTemplierServer(ctx, st, reload)
		if err != nil {
			err = fmt.Errorf("running templier server: %w", err)
			e.logger.Error(err.Error())
		}
		e.logger.Debug("templier server stopped")
		return err
	})

	wg.Add(1)
	errgrp.Go(func() error {
		defer wg.Done()
		e.runAppLauncher(ctx, st, reload)
		e.logger.Debug("app launcher stopped")
		return nil
	})

	// Only the debouncer starts builds after the initial one, so once it stops,
	// no new build can start while Run waits for the running ones.
	debouncer, debounced := debounce.NewSync(e.conf.Debounce)
	wg.Go(func() { debouncer(ctx) })

	// Initial build: run all custom watcher commands,
	// then if they succeed, lint & build.
	for i, w := range e.conf.CustomWatchers {
		e.runCustomWatcherCmd(ctx, st, i, w.Cmd, w.FailOnErr)
	}

	// Finalize initial build.
	if binaryFile := e.lintAndBuildServer(ctx, st, tempDirPath); binaryFile != "" {
		// Launch only when there are no errors on initial build.
		e.chRunNewServer <- binaryFile
	}

	handler := &fileChangeHandler{
		engine:         e,
		customWatchers: customWatchers,
		stateTracker:   st,
		reload:         reload,
		debounced:      debounced,
		buildRunner:    ctxrun.New(),
		build: func(ctx context.Context) (newBinaryPath string) {
			return e.lintAndBuildServer(ctx, st, tempDirPath)
		},
	}

	handler.watchBasePath, err = filepath.Abs(e.conf.App.DirWork)
	if err != nil {
		return fmt.Errorf("determining absolute base file path: %w", err)
	}
	e.logger.Debug("set absolute base file path", "path", handler.watchBasePath)

	fsWatcher, err := watcher.New(e.conf.App.DirSrcRoot, e.logger, handler.handle)
	if err != nil {
		return fmt.Errorf("initializing file watcher: %w", err)
	}

	wg.Add(1)
	errgrp.Go(func() error {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return nil
			case c := <-templChange:
				switch c {
				case cmdrun.TemplChangeNeedsRestart:
					handler.recompile(ctx, fsnotify.Event{})
				case cmdrun.TemplChangeNeedsBrowserReload:
					// Don't reload browser tabs while the app server is restarting.
					if !e.rerunActive.Load() {
						reload.BroadcastNonblock()
					}
				}
			}
		}
	})

	// NOTE: app.Exclude patterns are NOT applied via fsWatcher.Ignore
	// because that would suppress all events for excluded paths, preventing
	// custom watchers from reacting to changes in excluded directories.
	// Instead, app.Exclude is checked in fileChangeHandler.handle to skip
	// the default rebuild/restart while still letting custom watchers run.

	// Ignore templ temp files generated by `templ fmt`.
	if err := fsWatcher.Ignore("*.templ[0-9]*"); err != nil {
		return fmt.Errorf(
			`adding ignore templ temp files filter to watcher ("*.templ*"): %w`, err)
	}

	// Must run before fsWatcher.Add so matched subtrees skip both the
	// directory-add walk and the file-registration walk.
	for _, pattern := range e.conf.WatcherIgnore {
		if err := fsWatcher.Ignore(pattern); err != nil {
			return fmt.Errorf(
				"adding WatcherIgnore[%q] filter to watcher: %w", pattern, err)
		}
	}

	if err := fsWatcher.Add(e.conf.App.DirSrcRoot); err != nil {
		return fmt.Errorf("setting up file watcher for DirSrcRoot(%q): %w",
			e.conf.App.DirSrcRoot, err)
	}

	wg.Add(1)
	errgrp.Go(func() error {
		defer wg.Done()
		err := fsWatcher.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			err = fmt.Errorf("running file watcher: %w", err)
			e.logger.Error(err.Error())
		}
		e.logger.Debug("file watcher stopped")
		return err
	})

	{
		templierBaseURL := url.URL{
			Scheme: "http",
			Host:   e.conf.TemplierHost,
		}
		if e.conf.TLS != nil {
			templierBaseURL.Scheme = "https"
		}
		e.logger.Info("templier started", "url", templierBaseURL.String())
	}

	runErr := errgrp.Wait()
	if runErr != nil {
		e.logger.Debug("sub-process failure", "err", runErr)
	}
	e.logger.Debug("waiting for remaining sub-processes to shut down")
	wg.Wait()
	// Let canceled builds remove their temporary files first.
	handler.buildRunner.Wait()
	// Return the sub-process error (e.g. an HTTP server that failed to bind the
	// templier port) instead of swallowing it. Without this the caller can't
	// distinguish a normal shutdown from a fatal startup failure.
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return runErr
	}
	return nil
}

func (e *Engine) clearLogs() {
	if e.onClearLogs != nil {
		e.onClearLogs()
	}
}

func (e *Engine) runTemplierServer(
	ctx context.Context,
	st *statetrack.Tracker,
	reload *broadcaster.SignalBroadcaster,
) error {
	customWatcherNames := make([]string, len(e.conf.CustomWatchers))
	for i, w := range e.conf.CustomWatchers {
		customWatcherNames[i] = w.Name
	}

	httpSrv := http.Server{
		Addr: e.conf.TemplierHost,
		Handler: server.New(
			st,
			reload,
			e.logger,
			server.Config{
				PrintJSDebugLogs:   e.conf.Log.PrintJSDebugLogs,
				AppHost:            e.conf.App.Host,
				ProxyTimeout:       e.conf.ProxyTimeout,
				CustomWatcherNames: customWatcherNames,
				ReconnectMessage:   e.conf.ReconnectMessage,
			},
		),
		// Ends WebSocket connections, which Close leaves open.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	// Use WithContext so a ListenAndServe failure
	// (e.g. EADDRINUSE from a port already bound by another templier instance)
	// cancels the paired Shutdown goroutine immediately, instead of it blocking on
	// the parent context. Without this the outer Run hangs because its own Wait is
	// stuck on this function returning.
	errgrp, srvCtx := errgroup.WithContext(ctx)
	errgrp.Go(func() error {
		var err error
		if e.conf.TLS != nil {
			err = httpSrv.ListenAndServeTLS(e.conf.TLS.Cert, e.conf.TLS.Key)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	errgrp.Go(func() error {
		<-srvCtx.Done()
		// Close, not Shutdown: SSE streams and keep-alives would
		// stall Shutdown until its timeout. Browser reconnects.
		return httpSrv.Close()
	})
	err := errgrp.Wait()
	// http.Server.Close returns ErrServerClosed from the concurrent
	// ListenAndServe call; that's just the normal shutdown signal.
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (e *Engine) runAppLauncher(
	ctx context.Context,
	stateTracker *statetrack.Tracker,
	reload *broadcaster.SignalBroadcaster,
) {
	var latestSrvCmd *exec.Cmd
	var latestBinaryPath string
	var waitExit sync.WaitGroup

	// srvExited indicates that no app server process is currently running,
	// either because none was started yet or because it already exited.
	var srvExited atomic.Bool
	srvExited.Store(true)

	// stopping is true once templier stops the app server,
	// so its exit isn't reported as a crash.
	var stopping atomic.Bool

	stopServer := func() (stopped bool) {
		if latestSrvCmd == nil || latestSrvCmd.Process == nil || srvExited.Load() {
			return false // No app server process to stop.
		}
		stopping.Store(true)
		e.logger.Debug("stopping app server", "pid", latestSrvCmd.Process.Pid)
		graceful, err := cmdrun.StopProcess(latestSrvCmd.Process)
		if err != nil {
			if srvExited.Load() {
				// The process exited on its own in the meantime, for example
				// because it received the interrupt directly from the console.
				return false
			}
			e.logger.Error("stopping app server", "err", err)
			return false
		}
		if !graceful {
			e.logger.Debug("app server couldn't be stopped gracefully, terminated")
		}
		return true
	}

	// awaitServerExit waits for the app server process to exit and kills it
	// if it doesn't do so within serverShutdownTimeout to make sure a process
	// that refuses to stop can never block the launcher indefinitely.
	awaitServerExit := func() {
		if waitTimeout(&waitExit, serverShutdownTimeout) {
			return
		}
		e.logger.Error("app server didn't exit in time, killing it",
			"timeout", serverShutdownTimeout)
		if latestSrvCmd != nil && latestSrvCmd.Process != nil {
			if err := latestSrvCmd.Process.Kill(); err != nil {
				e.logger.Error("killing app server", "err", err)
			}
		}
		waitExit.Wait()
	}

	healthCheckClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}

	rerun := func(ctx context.Context) {
		e.rerunActive.Store(true)
		defer e.rerunActive.Store(false)

		start := time.Now()
		stopped := stopServer()
		awaitServerExit()

		if stopped {
			e.logger.Info("stopped server", "duration", time.Since(start))
		}

		if stateTracker.ErrIndex() != -1 {
			return // There's some error, we can't rerun now.
		}
		if ctx.Err() != nil {
			return // Canceled.
		}

		c := exec.Command(latestBinaryPath)
		if e.conf.App.Flags != nil {
			c.Args = append(c.Args, e.conf.App.Flags...)
		}
		c.Dir = e.conf.App.DirWork

		// Enable templ's development mode to read from .txt
		// for faster reloads without recompilation.
		c.Env = append(os.Environ(), "TEMPL_DEV_MODE=true")

		// Required for the app server to be stoppable gracefully on Windows.
		cmdrun.SetProcessGroup(c)

		var bufOutputCombined bytes.Buffer

		c.Stdout = io.MultiWriter(e.stdout, &bufOutputCombined)
		c.Stderr = io.MultiWriter(e.stderr, &bufOutputCombined)
		latestSrvCmd = c

		e.logger.Info("restarting server", "cmd", e.conf.App.DirCmd)

		e.logger.Debug("running app server command", "command", latestSrvCmd.String())
		stopping.Store(false)
		if err := c.Start(); err != nil {
			e.logger.Error("running app server",
				"cmd", e.conf.App.DirCmd, "err", err)
			return
		}
		e.logger.Debug("app server running", "pid", c.Process.Pid)
		srvExited.Store(false)

		waitExit.Go(func() {
			_ = c.Wait()
			srvExited.Store(true)
			if stopping.Load() || ctx.Err() != nil {
				return // Stopped by templier.
			}
			// Show the crash in the browser.
			e.logger.Error("app server exited", "status", c.ProcessState)
			msg := bufOutputCombined.String()
			if strings.TrimSpace(msg) == "" {
				msg = "app server exited: " + c.ProcessState.String()
			}
			stateTracker.Set(statetrack.IndexExit, msg)
			reload.BroadcastNonblock()
		})

		const maxRetries = 100
		for retry := 0; ; retry++ {
			if ctx.Err() != nil {
				e.logger.Debug("rerun canceled")
				return
			}
			if retry > maxRetries {
				e.logger.Error("waiting for server: retries failed",
					"retries", maxRetries)
				return
			}
			e.logger.Debug("health check",
				"retry", retry, "max", maxRetries,
				"method", http.MethodOptions,
				"url", e.conf.App.Host.String())
			r, err := http.NewRequest(
				http.MethodOptions, e.conf.App.Host.String(), http.NoBody,
			)
			r = r.WithContext(ctx)
			if err != nil {
				e.logger.Error("initializing preflight request", "err", err)
				continue
			}
			resp, err := healthCheckClient.Do(r)
			if err == nil {
				_ = resp.Body.Close()
				e.logger.Debug("health check: OK, app server is ready")
				break
			}
			if errors.Is(err, context.Canceled) {
				return
			}
			e.logger.Debug("health check error", "err", err)
			if srvExited.Load() {
				return // The exit is reported above.
			}
			e.logger.Debug("health check: waiting",
				"interval", serverHealthPreflightWaitInterval)
			time.Sleep(serverHealthPreflightWaitInterval)
		}

		if e.conf.Log.ClearOn == LogClearOnRestart {
			e.clearLogs()
		}

		if stopped {
			e.logger.Info("restarted server", "duration", time.Since(start))
		} else {
			e.logger.Info("started server", "duration", time.Since(start))
		}

		// Notify all clients to reload the page.
		reload.BroadcastNonblock()
	}

	runner := ctxrun.New()
	for {
		select {
		case <-e.chRerunServer:
			runner.Go(ctx, func(ctx context.Context) {
				rerun(ctx)
			})
		case newBinaryPath := <-e.chRunNewServer:
			if newBinaryPath == "" {
				e.logger.Debug("skip empty app server binary path")
				continue
			}
			runner.Go(ctx, func(ctx context.Context) {
				previousBinaryPath := latestBinaryPath
				latestBinaryPath = newBinaryPath
				rerun(ctx)
				if previousBinaryPath != "" {
					// The previous binary is removed only after rerun stopped
					// the process that was executing it. Windows refuses to
					// delete the executable file of a running process.
					e.logger.Debug("remove executable", "path", previousBinaryPath)
					if err := os.Remove(previousBinaryPath); err != nil {
						e.logger.Error("removing binary file",
							"path", previousBinaryPath, "err", err)
					}
				}
			})
		case <-ctx.Done():
			stopServer()
			// Wait for the app server process to actually exit before returning,
			// otherwise the cleanup would try to remove the executable file it's
			// still running, which Windows refuses to do.
			awaitServerExit()
			return
		}
	}
}

// fileChangeHandler handles file system change events.
type fileChangeHandler struct {
	engine         *Engine
	watchBasePath  string
	customWatchers []customWatcher
	stateTracker   *statetrack.Tracker
	reload         *broadcaster.SignalBroadcaster
	debounced      func(fn func())
	buildRunner    *ctxrun.Runner

	// build lints and builds the app server and returns the new binary's path,
	// or "" if none was built.
	build func(ctx context.Context) (newBinaryPath string)

	// lock protects the fields below.
	lock sync.Mutex
	// busy maps each custom watcher that's pending or running
	// to its latest trigger.
	busy map[int]uint64
	// triggers counts custom watcher triggers.
	triggers uint64
	// act is the strongest action the finished watchers require.
	act action.Type
	// heldBack is true if a build was held back while a watcher requiring
	// a rebuild was busy. It's merged into that rebuild rather than started
	// and then canceled by it.
	heldBack bool
}

// holdBack reports whether builds are held back and if so, records that one was.
// They're held back while a watcher requiring a rebuild is busy.
func (h *fileChangeHandler) holdBack() bool {
	h.lock.Lock()
	defer h.lock.Unlock()
	for i := range h.busy {
		if h.customWatchers[i].requires == action.ActionRebuild {
			h.heldBack = true
			return true
		}
	}
	return false
}

func (h *fileChangeHandler) handle(ctx context.Context, e fsnotify.Event) error {
	if e.Op == fsnotify.Chmod {
		h.engine.logger.Debug("ignoring file operation",
			"op", e.Op.String(), "name", e.Name)
		return nil
	}

	if h.engine.conf.Log.ClearOn == LogClearOnFileChange {
		h.engine.clearLogs()
	}

	relativeFileName, err := filepath.Rel(h.watchBasePath, e.Name)
	if err != nil {
		return fmt.Errorf(
			"determining relative path for %q with base path %q: %w",
			e.Name, h.watchBasePath, err)
	}

	h.engine.logger.Debug("handling file operation",
		"op", e.Op.String(), "name", relativeFileName)

	if h.engine.conf.Format && e.Op != fsnotify.Remove && e.Op != fsnotify.Rename {
		if strings.HasSuffix(e.Name, ".templ") {
			h.engine.logger.Debug("format templ file", "name", e.Name)
			templCmd := h.engine.conf.TemplCmd
			if err := cmdrun.RunTemplFmt(ctx, h.engine.conf.App.DirWork, templCmd, e.Name); err != nil {
				h.engine.logger.Error("templ formatting error", "err", err)
			}
		}
	}

	customWatcherTriggered := false
	for i, w := range h.customWatchers {
		if w.isFilePathIncluded(relativeFileName) {
			customWatcherTriggered = true
			h.triggerCustomWatcher(ctx, i, e)
		}
	}
	if customWatcherTriggered {
		// The watchers take the action they require once they're done.
		return nil
	}

	h.engine.logger.Debug("custom watchers: no watcher triggered")
	if strings.HasSuffix(e.Name, ".templ") {
		h.engine.logger.Debug("ignore templ file change", "name", e.Name)
		return nil
	}
	if h.stateTracker.Get(statetrack.IndexTempl) != "" {
		return nil
	}
	if strings.HasSuffix(e.Name, "_templ.go") {
		h.engine.logger.Debug("ignore _templ.go file change", "name", e.Name)
		return nil
	}

	if h.isExcluded(relativeFileName) {
		h.engine.logger.Debug("skip rebuild for excluded file", "name", relativeFileName)
		return nil
	}

	h.recompile(ctx, e)
	return nil
}

// triggerCustomWatcher schedules a run of custom watcher i for change e.
// Changes made during its debounce share one run.
func (h *fileChangeHandler) triggerCustomWatcher(
	ctx context.Context, i int, e fsnotify.Event,
) {
	h.lock.Lock()
	if h.busy == nil {
		h.busy = make(map[int]uint64)
	}
	h.triggers++
	trigger := h.triggers
	h.busy[i] = trigger
	h.lock.Unlock()

	h.customWatchers[i].debounced(func() {
		h.runCustomWatcher(ctx, i, e, trigger)
	})
}

// runCustomWatcher runs custom watcher i for the given trigger.
// The last busy watcher to finish takes the strongest action the watchers require.
func (h *fileChangeHandler) runCustomWatcher(
	ctx context.Context, i int, e fsnotify.Event, trigger uint64,
) {
	ok := h.runCustomWatcherCmd(ctx, i)

	h.lock.Lock()
	if h.busy[i] == trigger {
		// It wasn't triggered again meanwhile.
		delete(h.busy, i)
	}
	if ok {
		h.act = max(h.act, h.customWatchers[i].requires)
	}
	if len(h.busy) > 0 {
		h.lock.Unlock()
		return
	}
	act, heldBack := h.act, h.heldBack
	h.act, h.heldBack = action.ActionNone, false
	h.lock.Unlock()

	if heldBack {
		// Rebuild whatever the watchers require.
		// A rebuild restarts and reloads anyway.
		h.engine.logger.Debug("custom watchers: rebuild app server (held back build)")
		h.recompile(ctx, e)
		return
	}
	switch act {
	case action.ActionNone:
		h.engine.logger.Debug("custom watchers: no action")
	case action.ActionReload:
		h.engine.logger.Debug("custom watchers: notify reload")
		h.reload.BroadcastNonblock()
	case action.ActionRestart:
		h.engine.logger.Debug("custom watchers: rerun app server")
		select {
		case h.engine.chRerunServer <- struct{}{}:
		case <-ctx.Done(): // The launcher may have stopped.
		}
	case action.ActionRebuild:
		h.engine.logger.Debug("custom watchers: rebuild app server")
		h.recompile(ctx, e)
	}
}

// runCustomWatcherCmd runs custom watcher i's command, if any,
// and reports whether it succeeded.
func (h *fileChangeHandler) runCustomWatcherCmd(ctx context.Context, i int) (ok bool) {
	w := h.customWatchers[i]
	start := time.Now()
	defer func() {
		h.engine.logger.Info(w.name, "duration", time.Since(start))
	}()
	if w.cmd == "" {
		h.stateTracker.Set(statetrack.IndexOffsetCustomWatcher+i, "")
		return true
	}
	ok = h.engine.runCustomWatcherCmd(ctx, h.stateTracker, i, w.cmd, w.failOnErr)
	if !ok && w.failOnErr {
		h.reload.BroadcastNonblock()
	}
	return ok
}

// runCustomWatcherCmd runs custom watcher i's cmd and reports whether it succeeded.
// A failure is logged and, with failOnErr, shown in the browser.
func (e *Engine) runCustomWatcherCmd(
	ctx context.Context, st *statetrack.Tracker, i int, cmd string, failOnErr bool,
) (ok bool) {
	out, err := cmdrun.Sh(ctx, e.conf.App.DirWork, e.logger, cmd)
	if ctx.Err() != nil {
		return false // Canceled.
	}
	if err != nil {
		e.logger.Error("custom watcher failed",
			"watcher", cmd, "err", err, "output", string(out))
		if failOnErr {
			st.Set(statetrack.IndexOffsetCustomWatcher+i, cmdrun.FailureMessage(out, err))
		}
		return false
	}
	st.Set(statetrack.IndexOffsetCustomWatcher+i, "")
	return true
}

func (h *fileChangeHandler) isExcluded(relPath string) bool {
	relPath = filepath.ToSlash(relPath)
	for _, pattern := range h.engine.conf.App.Exclude {
		// Error is only ErrBadPattern, which can't happen because
		// patterns are validated during config parsing.
		if matched, _ := doublestar.Match(pattern, relPath); matched {
			return true
		}
	}
	return false
}

func (h *fileChangeHandler) recompile(ctx context.Context, e fsnotify.Event) {
	h.debounced(func() {
		if e.Op != 0 {
			h.engine.logger.Info("file changed",
				"op", e.Op.String(), "name", e.Name)
		}
		if h.holdBack() {
			h.engine.logger.Debug("hold back build until custom watchers finish")
			return
		}
		h.buildRunner.Go(ctx, func(ctx context.Context) {
			newBinaryPath := h.build(ctx)
			if h.stateTracker.ErrIndex() != -1 {
				h.engine.removeBinary(newBinaryPath)
				h.reload.BroadcastNonblock()
				return
			}
			if newBinaryPath == "" {
				return
			}
			if ctx.Err() == nil {
				select {
				case h.engine.chRunNewServer <- newBinaryPath:
					return
				case <-ctx.Done():
				}
			}
			// A newer build superseded this one, or templier is stopping.
			h.engine.removeBinary(newBinaryPath)
		})
	})
}

func (e *Engine) runGolangCILint(ctx context.Context, st *statetrack.Tracker) {
	startLinting := time.Now()
	buf, err := cmdrun.Run(
		ctx, e.conf.App.DirWork, nil, e.logger,
		"golangci-lint", "run", e.conf.App.DirSrcRoot+"/...",
	)
	if errors.Is(ctx.Err(), context.Canceled) {
		e.logger.Debug("golangci-lint cmd aborted")
		return
	}
	if errors.Is(err, cmdrun.ErrExitCode1) {
		bufStr := string(buf)
		e.logger.Error(bufStr)
		st.Set(statetrack.IndexGolangciLint, bufStr)
		return
	} else if err != nil {
		e.logger.Error("failed running golangci-lint", "err", err, "output", string(buf))
		return
	}
	st.Set(statetrack.IndexGolangciLint, "")
	e.logger.Info("linted", "duration", time.Since(startLinting))
}

func (e *Engine) buildServer(
	ctx context.Context, st *statetrack.Tracker, outBasePath string,
) (newBinaryPath string) {
	startBuilding := time.Now()

	binaryPath := makeUniqueServerOutPath(outBasePath)

	var compilerFlags, compilerEnv []string
	if e.conf.Compiler != nil {
		compilerFlags = e.conf.Compiler.Flags
		compilerEnv = e.conf.Compiler.Env
	}

	// go build doesn't remove its work directory when it's killed,
	// so give each build its own GOTMPDIR and remove it afterwards.
	env := compilerEnv
	if goTmpDir, err := os.MkdirTemp(e.goTmpDir, "templier-build-*"); err != nil {
		e.logger.Error("creating go build temporary directory", "err", err)
	} else {
		defer e.removeGoTmpDir(goTmpDir)
		// Appended last to override any GOTMPDIR in compilerEnv.
		env = slices.Concat(compilerEnv, []string{"GOTMPDIR=" + goTmpDir})
	}

	args := append([]string{"build"}, compilerFlags...)
	args = append(args, "-o", binaryPath, e.conf.App.DirCmd)
	buf, err := cmdrun.Run(ctx, "", env, e.logger, "go", args...)
	if errors.Is(ctx.Err(), context.Canceled) {
		e.logger.Debug("go build cmd aborted")
		// go build may have written the binary before it was canceled.
		e.removeBinary(binaryPath)
		return
	}
	if err != nil {
		msg := cmdrun.FailureMessage(buf, err)
		e.logger.Error(msg)
		st.Set(statetrack.IndexGo, msg)
		return
	}
	// Reset the process exit and go compiler errors.
	st.Set(statetrack.IndexGo, "")
	st.Set(statetrack.IndexExit, "")
	e.logger.Info("compiled cmd/server", "duration", time.Since(startBuilding))
	return binaryPath
}

// resolveGoTmpDir returns where each go build gets its own GOTMPDIR:
// the GOTMPDIR set by the user (in Compiler.Env, the environment
// or the go env file), or defaultDir if there's none.
func (e *Engine) resolveGoTmpDir(ctx context.Context, defaultDir string) string {
	var compilerEnv []string
	if e.conf.Compiler != nil {
		compilerEnv = e.conf.Compiler.Env
	}
	dir, err := cmdrun.RunGoEnv(ctx, "", compilerEnv, e.logger, "GOTMPDIR")
	if err != nil {
		e.logger.Error("reading GOTMPDIR from go env", "err", err)
		return defaultDir
	}
	if dir == "" {
		return defaultDir
	}
	return dir
}

// removeGoTmpDir removes a finished go build's GOTMPDIR. It retries because
// the compiler and linker processes of a killed build keep running
// and may still write to it for a short while.
func (e *Engine) removeGoTmpDir(path string) {
	const attempts, retryInterval = 20, 100 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := os.RemoveAll(path)
		if err == nil {
			return
		}
		if attempt == attempts {
			e.logger.Error("removing go build temporary directory",
				"path", path, "err", err)
			return
		}
		time.Sleep(retryInterval)
	}
}

// removeBinary removes an app server binary that won't be run, if any.
func (e *Engine) removeBinary(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		e.logger.Error("removing binary file", "path", path, "err", err)
	}
}

func makeUniqueServerOutPath(basePath string) string {
	tm := time.Now()
	name := "server_" + strconv.FormatInt(tm.UnixNano(), 16)
	if runtime.GOOS == "windows" {
		// Windows resolves executables by extension,
		// a file without one can't be executed even when the full path is provided.
		name += ".exe"
	}
	return filepath.Join(basePath, name)
}

func (e *Engine) lintAndBuildServer(
	ctx context.Context, st *statetrack.Tracker, outBasePath string,
) (newBinaryPath string) {
	if st.ErrIndex() == statetrack.IndexTempl {
		return
	}
	var wg sync.WaitGroup
	if e.conf.Lint {
		wg.Go(func() {
			e.runGolangCILint(ctx, st)
		})
	}
	wg.Go(func() {
		newBinaryPath = e.buildServer(ctx, st, outBasePath)
		if newBinaryPath != "" {
			e.logger.Debug("new app server binary", "path", newBinaryPath)
		}
	})
	wg.Wait()
	return newBinaryPath
}

func (e *Engine) checkTemplVersion(ctx context.Context) error {
	version, err := cmdrun.RunTemplVersion(ctx, "", e.conf.TemplCmd, e.logger)
	if err != nil {
		return err
	}
	if supported := supportedTemplVersion(); !strings.HasPrefix(version, supported) {
		e.logger.Warn("unsupported templ version",
			"templier_version", e.version,
			"supported_templ_version", supported,
			"current_templ_version", version)
	}
	return nil
}

// customWatcher matches file paths against include/exclude globs
// and executes a command when triggered.
type customWatcher struct {
	name      string
	cmd       string
	include   []string
	exclude   []string
	debounced func(func())
	failOnErr bool
	requires  action.Type
}

func (c customWatcher) isFilePathIncluded(s string) bool {
	s = filepath.ToSlash(s)
	for _, pattern := range c.include {
		if matched, _ := doublestar.Match(pattern, s); matched {
			for _, pattern := range c.exclude {
				if matched, _ := doublestar.Match(pattern, s); matched {
					return false
				}
			}
			return true
		}
	}
	return false
}
