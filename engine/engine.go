package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
			ctx, e.conf.App.DirSrcRoot, e.logger, st, templChange,
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
		go debouncer(ctx)

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

	debouncer, debounced := debounce.NewSync(e.conf.Debounce)
	go debouncer(ctx)

	// Initial build: run all custom watcher commands,
	// then if they succeed, lint & build.
	for i, w := range e.conf.CustomWatchers {
		o, err := cmdrun.Sh(ctx, e.conf.App.DirWork, e.logger, w.Cmd)
		output := string(o)
		if errors.Is(err, cmdrun.ErrExitCode1) {
			if !w.FailOnErr {
				e.logger.Error("custom watcher exited with code 1",
					"watcher", w.Cmd, "output", output)
				continue
			}
			st.Set(statetrack.IndexOffsetCustomWatcher+i, output)
			continue
		} else if err != nil {
			e.logger.Error("running custom watcher cmd",
				"watcher", w.Cmd, "err", err)
			continue
		}
		st.Set(statetrack.IndexOffsetCustomWatcher+i, "")
	}

	// Finalize initial build.
	if binaryFile := e.lintAndBuildServer(ctx, st, tempDirPath); binaryFile != "" {
		// Launch only when there are no errors on initial build.
		e.chRunNewServer <- binaryFile
	}

	handler := &fileChangeHandler{
		engine:            e,
		binaryOutBasePath: tempDirPath,
		customWatchers:    customWatchers,
		stateTracker:      st,
		reload:            reload,
		debounced:         debounced,
		buildRunner:       ctxrun.New(),
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

	for _, expr := range e.conf.App.Exclude {
		if err := fsWatcher.Ignore(expr); err != nil {
			return fmt.Errorf("adding ignore filter to watcher (%q): %w", expr, err)
		}
	}

	// Ignore templ temp files generated by `templ fmt`.
	if err := fsWatcher.Ignore("*.templ[0-9]*"); err != nil {
		return fmt.Errorf(
			`adding ignore templ temp files filter to watcher ("*.templ*"): %w`, err)
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

	if err := errgrp.Wait(); err != nil {
		e.logger.Debug("sub-process failure", "err", err)
	}
	e.logger.Debug("waiting for remaining sub-processes to shut down")
	wg.Wait()
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
			&http.Client{Timeout: e.conf.ProxyTimeout},
			st,
			reload,
			e.logger,
			server.Config{
				PrintJSDebugLogs:   e.conf.Log.PrintJSDebugLogs,
				AppHost:            e.conf.App.Host,
				ProxyTimeout:       e.conf.ProxyTimeout,
				CustomWatcherNames: customWatcherNames,
			},
		),
	}

	var errgrp errgroup.Group
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
		<-ctx.Done()
		return httpSrv.Shutdown(ctx)
	})
	return errgrp.Wait()
}

func (e *Engine) runAppLauncher(
	ctx context.Context,
	stateTracker *statetrack.Tracker,
	reload *broadcaster.SignalBroadcaster,
) {
	var latestSrvCmd *exec.Cmd
	var latestBinaryPath string
	var waitExit sync.WaitGroup

	stopServer := func() (stopped bool) {
		if latestSrvCmd == nil || latestSrvCmd.Process == nil {
			return false
		}
		e.logger.Debug("stopping app server", "pid", latestSrvCmd.Process.Pid)
		if err := latestSrvCmd.Process.Signal(os.Interrupt); err != nil {
			e.logger.Error("sending interrupt signal to app server", "err", err)
			return false
		}
		return true
	}

	healthCheckClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}

	rerun := func(ctx context.Context) {
		e.rerunActive.Store(true)
		defer e.rerunActive.Store(false)

		start := time.Now()
		stopped := stopServer()
		waitExit.Wait()

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

		var bufOutputCombined bytes.Buffer

		c.Stdout = io.MultiWriter(e.stdout, &bufOutputCombined)
		c.Stderr = io.MultiWriter(e.stderr, &bufOutputCombined)
		latestSrvCmd = c

		e.logger.Info("restarting server", "cmd", e.conf.App.DirCmd)

		e.logger.Debug("running app server command", "command", latestSrvCmd.String())
		if err := c.Start(); err != nil {
			e.logger.Error("running app server",
				"cmd", e.conf.App.DirCmd, "err", err)
		}
		if c.Process != nil {
			e.logger.Debug("app server running", "pid", c.Process.Pid)
		}

		var exitCode atomic.Int32
		exitCode.Store(-1)
		waitExit.Go(func() {
			err := c.Wait()
			if err == nil {
				return
			}
			if exitError, ok := err.(*exec.ExitError); ok {
				exitCode.Store(int32(exitError.ExitCode()))
				return
			}
			e.logger.Error("health check: waiting for process", "err", err)
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
			if code := exitCode.Load(); code != -1 && code != 0 {
				e.logger.Error("health check: app server exited",
					"exit_code", code)
				stateTracker.Set(statetrack.IndexExit, bufOutputCombined.String())
				return
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
			runner.Go(ctx, func(ctx context.Context) {
				if latestBinaryPath != "" {
					e.logger.Debug("remove executable", "path", latestBinaryPath)
					if err := os.Remove(latestBinaryPath); err != nil {
						e.logger.Error("removing binary file",
							"path", latestBinaryPath, "err", err)
					}
				}
				latestBinaryPath = newBinaryPath
				rerun(ctx)
			})
		case <-ctx.Done():
			stopServer()
			return
		}
	}
}

// fileChangeHandler handles file system change events.
type fileChangeHandler struct {
	engine            *Engine
	binaryOutBasePath string
	watchBasePath     string
	customWatchers    []customWatcher
	stateTracker      *statetrack.Tracker
	reload            *broadcaster.SignalBroadcaster
	debounced         func(fn func())
	buildRunner       *ctxrun.Runner
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
			if err := cmdrun.RunTemplFmt(ctx, h.engine.conf.App.DirWork, e.Name); err != nil {
				h.engine.logger.Error("templ formatting error", "err", err)
			}
		}
	}

	var wg sync.WaitGroup
	var customWatcherTriggered atomic.Bool
	var act action.SyncStatus

	if len(h.customWatchers) > 0 {
		wg.Add(len(h.customWatchers))
		for i, w := range h.customWatchers {
			if !w.isFilePathIncluded(relativeFileName) {
				wg.Done()
				continue
			}

			customWatcherTriggered.Store(true)
			index := i
			w.debounced(func() {
				defer wg.Done()
				start := time.Now()
				defer func() {
					h.engine.logger.Info(w.name,
						"duration", time.Since(start))
				}()
				if w.cmd != "" {
					o, err := cmdrun.Sh(ctx, h.engine.conf.App.DirWork, h.engine.logger, w.cmd)
					output := string(o)
					if errors.Is(err, cmdrun.ErrExitCode1) {
						if w.failOnErr {
							h.stateTracker.Set(
								statetrack.IndexOffsetCustomWatcher+index, output)
							h.reload.BroadcastNonblock()
						} else {
							h.engine.logger.Error("custom watcher exited with code 1",
								"watcher", w.cmd, "output", output)
						}
						return
					} else if err != nil {
						if w.failOnErr {
							h.stateTracker.Set(
								statetrack.IndexOffsetCustomWatcher+index, output)
						}
						h.engine.logger.Error("executing custom watcher",
							"watcher", w.cmd, "output", output)
					}
				}
				h.stateTracker.Set(statetrack.IndexOffsetCustomWatcher+index, "")
				act.Require(w.requires)
			})
		}
	}

	wg.Wait()
	if customWatcherTriggered.Load() {
		switch act.Load() {
		case action.ActionNone:
			h.engine.logger.Debug("custom watchers: no action")
			return nil
		case action.ActionReload:
			h.engine.logger.Debug("custom watchers: notify reload")
			h.reload.BroadcastNonblock()
			return nil
		case action.ActionRestart:
			h.engine.logger.Debug("custom watchers: rerun app server")
			h.engine.chRerunServer <- struct{}{}
			return nil
		default:
			h.engine.logger.Debug("custom watchers: rebuild app server")
		}
	} else {
		h.engine.logger.Debug("custom watchers: no watcher triggered")
		if strings.HasSuffix(e.Name, ".templ") {
			h.engine.logger.Debug("ignore templ file change", "name", e.Name)
			return nil
		}
		if h.stateTracker.Get(statetrack.IndexTempl) != "" {
			return nil
		}
	}
	if strings.HasSuffix(e.Name, "_templ.go") {
		h.engine.logger.Debug("ignore _templ.go file change", "name", e.Name)
		return nil
	}

	h.recompile(ctx, e)
	return nil
}

func (h *fileChangeHandler) recompile(ctx context.Context, e fsnotify.Event) {
	h.debounced(func() {
		if e.Op != 0 {
			h.engine.logger.Info("file changed",
				"op", e.Op.String(), "name", e.Name)
		}
		h.buildRunner.Go(ctx, func(ctx context.Context) {
			newBinaryPath := h.engine.lintAndBuildServer(
				ctx, h.stateTracker, h.binaryOutBasePath,
			)
			if h.stateTracker.ErrIndex() != -1 {
				h.reload.BroadcastNonblock()
				return
			}
			h.engine.chRunNewServer <- newBinaryPath
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
		e.logger.Error("failed running golangci-lint", "err", err)
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

	args := append([]string{"build"}, compilerFlags...)
	args = append(args, "-o", binaryPath, e.conf.App.DirCmd)
	buf, err := cmdrun.Run(ctx, "", compilerEnv, e.logger, "go", args...)
	if errors.Is(ctx.Err(), context.Canceled) {
		e.logger.Debug("go build cmd aborted")
		return
	}
	if err != nil {
		bufStr := string(buf)
		e.logger.Error(bufStr)
		st.Set(statetrack.IndexGo, bufStr)
		return
	}
	// Reset the process exit and go compiler errors.
	st.Set(statetrack.IndexGo, "")
	st.Set(statetrack.IndexExit, "")
	e.logger.Info("compiled cmd/server", "duration", time.Since(startBuilding))
	return binaryPath
}

func makeUniqueServerOutPath(basePath string) string {
	tm := time.Now()
	return filepath.Join(basePath, "server_"+strconv.FormatInt(tm.UnixNano(), 16))
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
	out, err := cmdrun.Run(ctx, "", nil, e.logger, "templ", "version")
	if err != nil {
		return err
	}
	outStr := strings.TrimSpace(string(out))
	if supported := supportedTemplVersion(); !strings.HasPrefix(outStr, supported) {
		e.logger.Warn("unsupported templ version",
			"templier_version", e.version,
			"supported_templ_version", supported,
			"current_templ_version", outStr)
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
