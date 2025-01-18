package main

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/romshark/templier/internal/action"
	"github.com/romshark/templier/internal/broadcaster"
	"github.com/romshark/templier/internal/cmdrun"
	"github.com/romshark/templier/internal/config"
	"github.com/romshark/templier/internal/ctxrun"
	"github.com/romshark/templier/internal/debounce"
	"github.com/romshark/templier/internal/fswalk"
	"github.com/romshark/templier/internal/log"
	"github.com/romshark/templier/internal/server"
	"github.com/romshark/templier/internal/statetrack"
	"github.com/romshark/templier/internal/templgofilereg"
	"github.com/romshark/templier/internal/watcher"
	"golang.org/x/sync/errgroup"

	"github.com/fsnotify/fsnotify"
	"github.com/gobwas/glob"
)

const ServerHealthPreflightWaitInterval = 100 * time.Millisecond

var (
	chRerunServer  = make(chan struct{}, 1)
	chRunNewServer = make(chan string, 1)
)

type customWatcher struct {
	name      string
	cmd       config.CmdStr
	include   []glob.Glob
	exclude   []glob.Glob
	debounced func(func())
	failOnErr bool
	requires  action.Type
}

func (c customWatcher) isFilePathIncluded(s string) bool {
	for _, glob := range c.include {
		if glob.Match(s) {
			for _, glob := range c.exclude {
				if glob.Match(s) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func main() {
	conf := config.MustParse()
	log.SetLogLevel(log.LogLevel(conf.Log.Level))

	if err := checkTemplVersion(context.Background()); err != nil {
		log.Fatalf("checking templ version: %v", err)
	}

	// Make sure required cmds are available.
	if _, err := exec.LookPath("templ"); err != nil {
		log.FatalCmdNotAvailable(
			"templ", "https://templ.guide/quick-start/installation",
		)
	}
	if conf.Lint {
		if _, err := exec.LookPath("golangci-lint"); err != nil {
			log.FatalCmdNotAvailable(
				"golangci-lint",
				"https://github.com/golangci/golangci-lint"+
					"?tab=readme-ov-file#install-golangci-lint",
			)
		}
	}
	for _, w := range conf.CustomWatchers {
		if w.Cmd == "" {
			continue
		}
		cmd := w.Cmd.Cmd()
		if _, err := exec.LookPath(cmd); err != nil {
			log.FatalCustomWatcherCmdNotAvailable(cmd, string(w.Name))
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Create a temporary directory
	tempDirPath, err := os.MkdirTemp("", "templier-*")
	if err != nil {
		log.Fatalf("creating temporary directory: %v\n", err)
	}
	log.Debugf("set server binaries output path: %q", tempDirPath)
	defer func() {
		log.Debugf("removing server binaries output directory: %s", tempDirPath)
		if err := os.RemoveAll(tempDirPath); err != nil {
			log.Errorf("deleting temporary directory: %v\n", err)
		}
	}()

	// At this point, use of fatal logging is no longer allowed.
	// Use panic instead to ensure deferred functions are executed.

	reload := broadcaster.NewSignalBroadcaster()
	st := statetrack.NewTracker(len(conf.CustomWatchers))

	errgrp, ctx := errgroup.WithContext(ctx)
	var wg sync.WaitGroup

	wg.Add(1)
	errgrp.Go(func() error {
		defer wg.Done()
		// Run templ in watch mode to create debug components.
		// Once ctx is canceled templ will write production output and exit.
		err := cmdrun.RunTemplWatch(ctx, conf.App.DirSrcRootAbsolute(), st)
		if err != nil && !errors.Is(err, context.Canceled) {
			err = fmt.Errorf("running 'templ generate --watch': %w", err)
			log.Error(err.Error())
		}
		log.Debugf("'templ generate --watch' stopped")
		return err
	})

	// Initialize custom customWatchers.
	customWatchers := make([]customWatcher, len(conf.CustomWatchers))
	for i, w := range conf.CustomWatchers {
		debouncer, debounced := debounce.NewSync(w.Debounce)
		go debouncer(ctx)

		// The following globs have already been validated during config parsing.
		// It's safe to assume compilation succeeds.
		include := make([]glob.Glob, len(w.Include))
		for i, pattern := range w.Include {
			include[i] = glob.MustCompile(pattern)
		}
		exclude := make([]glob.Glob, len(w.Exclude))
		for i, pattern := range w.Exclude {
			exclude[i] = glob.MustCompile(pattern)
		}

		customWatchers[i] = customWatcher{
			name:      string(w.Name),
			debounced: debounced,
			cmd:       w.Cmd,
			failOnErr: w.FailOnError,
			include:   include,
			exclude:   exclude,
			requires:  action.Type(w.Requires),
		}
	}

	wg.Add(1)
	errgrp.Go(func() error {
		defer wg.Done()
		err := runTemplierServer(ctx, st, reload, conf)
		if err != nil {
			err = fmt.Errorf("running templier server: %w", err)
			log.Error(err.Error())
		}
		log.Debugf("templier server stopped")
		return err
	})

	wg.Add(1)
	errgrp.Go(func() error {
		defer wg.Done()
		runAppLauncher(ctx, st, reload, conf)
		log.Debugf("app launcher stopped")
		return nil
	})

	debouncer, debounced := debounce.NewSync(conf.Debounce)
	go debouncer(ctx)

	// Initial build, run all custom watcher cmd's and if they succeed then lint & build
	for i, watcher := range conf.CustomWatchers {
		o, err := cmdrun.Sh(ctx, conf.App.DirWork, string(watcher.Cmd))
		output := string(o)
		if errors.Is(err, cmdrun.ErrExitCode1) {
			if !watcher.FailOnError {
				log.Errorf(
					"custom watcher %q exited with code 1: %s",
					watcher.Cmd, output,
				)
				continue
			}
			st.Set(statetrack.IndexOffsetCustomWatcher+i, string(o))
			continue
		} else if err != nil {
			log.Errorf("running custom watcher cmd %q: %v", watcher.Cmd, err)
			continue
		}
		st.Set(statetrack.IndexOffsetCustomWatcher+i, "")
	}

	// Finalize initial build
	if binaryFile := lintAndBuildServer(ctx, st, conf, tempDirPath); binaryFile != "" {
		// Launch only when there's no errors on initial build.
		chRunNewServer <- binaryFile
	}

	// Collect all _templ.go files and add them to the registry
	templGoFileReg := templgofilereg.New()
	if currentWorkingDir, err := os.Getwd(); err != nil {
		log.Errorf("getting current working directory: %v", err)
	} else {
		if err := fswalk.Files(currentWorkingDir, func(name string) error {
			if !strings.HasSuffix(name, "_templ.go") {
				return nil
			}
			if _, err := templGoFileReg.Compare(name); err != nil {
				log.Errorf("registering generated templ Go file: %q: %v", name, err)
			}
			log.Debugf("registered existing generated templ Go file: %q", name)
			return nil
		}); err != nil {
			log.Errorf("collecting existing _templ.go files: %v", err)
		}
	}

	onChangeHandler := FileChangeHandler{
		binaryOutBasePath:     tempDirPath,
		customWatchers:        customWatchers,
		stateTracker:          st,
		reload:                reload,
		debounced:             debounced,
		conf:                  conf,
		templGoFileRegistry:   templGoFileReg,
		validationBuildRunner: ctxrun.New(),
		buildRunner:           ctxrun.New(),
	}

	onChangeHandler.watchBasePath, err = filepath.Abs(conf.App.DirWork)
	if err != nil {
		panic(fmt.Errorf("determining absolute base file path: %v", err))
	}
	log.Debugf("set absolute base file path: %q", onChangeHandler.watchBasePath)

	watcher, err := watcher.New(conf.App.DirSrcRootAbsolute(), onChangeHandler.Handle)
	if err != nil {
		panic(fmt.Errorf("initializing file watcher: %w", err))
	}

	for _, expr := range conf.App.Exclude {
		if err := watcher.Ignore(expr); err != nil {
			panic(fmt.Errorf("adding ignore filter to watcher (%q): %w", expr, err))
		}
	}

	// Ignore templ temp files generated by `templ fmt`.
	if err := watcher.Ignore("*.templ[0-9]*"); err != nil {
		panic(fmt.Errorf(
			`adding ignore templ temp files filter to watcher ("*.templ*"): %w`,
			err,
		))
	}

	if err := watcher.Add(conf.App.DirSrcRootAbsolute()); err != nil {
		panic(fmt.Errorf("setting up file watcher for app.dir-src-root(%q): %w",
			conf.App.DirSrcRootAbsolute(), err))
	}

	wg.Add(1)
	errgrp.Go(func() error {
		defer wg.Done()
		err := watcher.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			err = fmt.Errorf("running file watcher: %w", err)
			log.Error(err.Error())
		}
		log.Debugf("file watcher stopped")
		return err
	})

	{
		templierBaseURL := url.URL{
			Scheme: "http",
			Host:   conf.TemplierHost,
		}
		if conf.TLS != nil {
			templierBaseURL.Scheme = "https"
		}

		log.TemplierStarted(templierBaseURL.String())
	}

	if err := errgrp.Wait(); err != nil {
		log.Debugf("sub-process failure: %v", err)
	}
	cancel() // Ask all sub-processes to exit gracefully.
	log.Debugf("waiting for remaining sub-processes to shut down")
	wg.Wait() // Wait for all sub-processes to exit.
}

func runTemplierServer(
	ctx context.Context,
	st *statetrack.Tracker,
	reload *broadcaster.SignalBroadcaster,
	conf *config.Config,
) error {
	httpSrv := http.Server{
		Addr: conf.TemplierHost,
		Handler: server.New(
			&http.Client{
				Timeout: conf.ProxyTimeout,
			},
			st,
			reload,
			conf,
		),
	}

	var errgrp errgroup.Group
	errgrp.Go(func() error {
		var err error
		if conf.TLS != nil {
			err = httpSrv.ListenAndServeTLS(conf.TLS.Cert, conf.TLS.Key)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	errgrp.Go(func() error {
		<-ctx.Done() // Wait for shutdown signal.
		return httpSrv.Shutdown(ctx)
	})

	return errgrp.Wait()
}

func runAppLauncher(
	ctx context.Context,
	stateTracker *statetrack.Tracker,
	reload *broadcaster.SignalBroadcaster,
	conf *config.Config,
) {
	var latestSrvCmd *exec.Cmd
	var latestBinaryPath string
	var waitExit sync.WaitGroup

	stopServer := func() {
		if latestSrvCmd == nil || latestSrvCmd.Process == nil {
			return
		}
		log.Debugf("stopping app server with pid %d", latestSrvCmd.Process.Pid)
		if err := latestSrvCmd.Process.Signal(os.Interrupt); err != nil {
			log.Errorf("sending interrupt signal to app server: %v", err)
			return
		}
	}

	healthCheckClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}

	rerun := func() {
		start := time.Now()
		stopServer()
		waitExit.Wait()

		log.Durf("stopped server", time.Since(start))

		if stateTracker.ErrIndex() != -1 {
			// There's some error, we can't rerun now.
			return
		}

		c := exec.Command(latestBinaryPath)
		if conf.App.Flags != nil {
			c.Args = append(c.Args, conf.App.Flags...)
		}

		// Enable templ's development mode to read from .txt
		// for faster reloads without recompilation.
		c.Env = append(os.Environ(), "TEMPL_DEV_MODE=true")

		var bufOutputCombined bytes.Buffer

		c.Stdout = io.MultiWriter(os.Stdout, &bufOutputCombined)
		c.Stderr = io.MultiWriter(os.Stderr, &bufOutputCombined)
		latestSrvCmd = c

		log.TemplierRestartingServer(conf.App.DirCmd)

		log.Debugf("running app server command: %s", latestSrvCmd.String())
		if err := c.Start(); err != nil {
			log.Errorf("running %s: %v", conf.App.DirCmd, err)
		}
		if c.Process != nil {
			log.Debugf("app server running (pid: %d)", c.Process.Pid)
		}

		var exitCode atomic.Int32
		exitCode.Store(-1)
		waitExit.Add(1)
		go func() {
			defer waitExit.Done()
			err := c.Wait()
			if err == nil {
				return
			}
			if exitError, ok := err.(*exec.ExitError); ok {
				// The program has exited with an exit code != 0
				exitCode.Store(int32(exitError.ExitCode()))
				return
			}
			// Some other error occurred
			log.Errorf("health check: waiting for process: %v", err)
		}()

		const maxRetries = 100
		for retry := 0; ; retry++ {
			if ctx.Err() != nil {
				// Canceled
				return
			}
			if retry > maxRetries {
				log.Errorf("waiting for server: %d retries failed", maxRetries)
				return
			}
			// Wait for the server to be ready
			log.Debugf("health check (%d/%d): %s %q",
				retry, maxRetries, http.MethodOptions, conf.App.Host.URL.String())
			r, err := http.NewRequest(
				http.MethodOptions, conf.App.Host.URL.String(), http.NoBody,
			)
			r = r.WithContext(ctx)
			if err != nil {
				log.Errorf("initializing preflight request: %v", err)
				continue
			}
			resp, err := healthCheckClient.Do(r)
			if err == nil {
				resp.Body.Close()
				log.Debugf("health check: OK, " +
					"app server is ready to receive requests")
				break // Server is ready to receive requests
			}
			log.Debugf("health check: err: %v", err)
			if code := exitCode.Load(); code != -1 && code != 0 {
				log.Errorf("health check: app server exited with exit code %d", code)
				stateTracker.Set(statetrack.IndexExit, bufOutputCombined.String())
				return
			}
			log.Debugf("health check: wait: %s", ServerHealthPreflightWaitInterval)
			time.Sleep(ServerHealthPreflightWaitInterval)
		}

		if conf.Log.ClearOn == config.LogClearOnRestart {
			log.ClearLogs()
		}
		log.Durf("restarted server", time.Since(start))

		// Notify all clients to reload the page
		reload.BroadcastNonblock()
	}

	for {
		select {
		case <-chRerunServer:
			rerun()
		case newBinaryPath := <-chRunNewServer:
			if latestBinaryPath != "" {
				log.Debugf("remove executable: %s", latestBinaryPath)
				if err := os.Remove(latestBinaryPath); err != nil {
					log.Errorf("removing binary file %q: %v", latestBinaryPath, err)
				}
			}
			latestBinaryPath = newBinaryPath
			rerun()
		case <-ctx.Done():
			stopServer()
			return
		}
	}
}

type FileChangeHandler struct {
	binaryOutBasePath     string
	watchBasePath         string
	customWatchers        []customWatcher
	stateTracker          *statetrack.Tracker
	reload                *broadcaster.SignalBroadcaster
	debounced             func(fn func())
	conf                  *config.Config
	templGoFileRegistry   *templgofilereg.Comparer
	validationBuildRunner *ctxrun.Runner
	buildRunner           *ctxrun.Runner
}

func (h *FileChangeHandler) Handle(ctx context.Context, e fsnotify.Event) error {
	switch e.Op {
	case fsnotify.Chmod:
		log.Debugf("ignoring file operation (%s): %q", e.Op.String(), e.Name)
		return nil // Ignore chmod events.
	case fsnotify.Remove:
		// No need to check for _templ.go suffix, Remove is a no-op for other files.
		h.templGoFileRegistry.Remove(e.Name)
	}

	if h.conf.Log.ClearOn == config.LogClearOnFileChange {
		log.ClearLogs()
	}

	relativeFileName, err := filepath.Rel(h.watchBasePath, e.Name)
	if err != nil {
		panic(fmt.Errorf(
			"determining relative path for %q with base path %q",
			e.Name, h.conf.App.DirWork,
		))
	}

	log.Debugf("handling file operation (%s): %q", e.Op.String(), relativeFileName)

	if h.conf.Format {
		if strings.HasSuffix(e.Name, ".templ") {
			log.Debugf("format templ file %s", e.Name)
			err := cmdrun.RunTemplFmt(context.Background(), h.conf.App.DirWork, e.Name)
			if err != nil {
				log.Errorf("templ formatting error: %v", err)
			}
		}
	}

	var wg sync.WaitGroup
	var customWatcherTriggered atomic.Bool
	var act action.SyncStatus

	if len(h.customWatchers) > 0 {
		// Each custom watcher will be executed in the goroutine of its debouncer.
		wg.Add(len(h.customWatchers))
		for i, w := range h.customWatchers {
			if !w.isFilePathIncluded(relativeFileName) {
				// File doesn't match any glob
				wg.Done()
				continue
			}

			customWatcherTriggered.Store(true)
			index := i
			w.debounced(func() { // This runs in a separate goroutine.
				defer wg.Done()
				start := time.Now()
				defer func() { log.Durf(string(w.name), time.Since(start)) }()
				if w.cmd != "" {
					o, err := cmdrun.Sh(ctx, h.conf.App.DirWork, string(w.cmd))
					output := string(o)
					if errors.Is(err, cmdrun.ErrExitCode1) {
						if w.failOnErr {
							h.stateTracker.Set(
								statetrack.IndexOffsetCustomWatcher+index, output,
							)
							h.reload.BroadcastNonblock()
						} else {
							// Log the error when fail-on-error is disabled.
							log.Errorf(
								"custom watcher %q exited with code 1: %s",
								w.cmd, output,
							)
						}
						return
					} else if err != nil {
						// The reason this cmd failed was not just exit code 1.
						if w.failOnErr {
							h.stateTracker.Set(
								statetrack.IndexOffsetCustomWatcher+index, output,
							)
						}
						log.Errorf(
							"executing custom watcher %q: %s",
							w.cmd, output,
						)
					}
				}
				h.stateTracker.Set(statetrack.IndexOffsetCustomWatcher+index, "")
				act.Require(w.requires)
			})
		}
	}

	wg.Wait() // Wait for all custom watcher to finish before attempting reload.
	if customWatcherTriggered.Load() {
		// Custom watcher was triggered, apply custom action.
		switch act.Load() {
		case action.ActionNone:
			// Custom watchers require no further action to be taken.
			log.Debugf("custom watchers: no action")
			return nil
		case action.ActionReload:
			// Custom watchers require just a reload of all browser tabs.
			log.Debugf("custom watchers: notify reload")
			h.reload.BroadcastNonblock()
			return nil
		case action.ActionRestart:
			// Custom watchers require just a server restart.
			log.Debugf("custom watchers: rerun app server")
			chRerunServer <- struct{}{}
			return nil
		default:
			log.Debugf("custom watchers: rebuild app server")
		}
	} else {
		log.Debugf("custom watchers: no watcher triggered")
		if strings.HasSuffix(e.Name, "_templ.txt") {
			log.Debugf("ignore change in generated templ txt: %s", e.Name)
			return nil
		}
		// No custom watcher triggered, follow default pipeline.
		if strings.HasSuffix(e.Name, ".templ") {
			h.buildRunner.Go(context.Background(), func(ctx context.Context) {
				// Try to build the server but don't cause reloads if it succeeds,
				// instead just report the errors because the reload or recompilation
				// will be done once the _templ.go file change is detected.
				// This is necessary when for example an argument was added to one of
				// the .templ files but the Go code using the generated render functions
				// doesn't yet pass this argument.
				binaryPath := lintAndBuildServer(
					ctx, h.stateTracker, h.conf, h.binaryOutBasePath,
				)
				if err := os.Remove(binaryPath); err != nil {
					log.Debugf("removing the temporary server binary: %v", err)
				}
				if ctx.Err() != nil {
					return // Canceled.
				}
				compiler := h.stateTracker.Get(statetrack.IndexGo)
				linter := h.stateTracker.Get(statetrack.IndexGolangciLint)
				if compiler != "" || linter != "" {
					h.reload.BroadcastNonblock()
				}
			})
			return nil
		}
		if h.stateTracker.Get(statetrack.IndexTempl) != "" {
			// A templ template is broken, don't continue.
			return nil
		}
		if strings.HasSuffix(e.Name, "_templ.go") {
			if recompile, err := h.templGoFileRegistry.Compare(e.Name); err != nil {
				log.Errorf("checking generated templ go file: %v", err)
				return nil
			} else if !recompile {
				log.Debugf("_templ.go change doesn't require recompilation")
				// Reload browser tabs when a _templ.go file has changed without
				// changing its code structure (load from _templ.txt is possible).
				h.reload.BroadcastNonblock()
				return nil
			}
			log.Debugf("change in _templ.go requires recompilation")
		}
	}

	h.debounced(func() {
		log.TemplierFileChange(e)
		h.buildRunner.Go(context.Background(), func(ctx context.Context) {
			newBinaryPath := lintAndBuildServer(
				ctx, h.stateTracker, h.conf, h.binaryOutBasePath,
			)
			if h.stateTracker.ErrIndex() != -1 {
				h.reload.BroadcastNonblock()
				// Don't restart the server if there was any error.
				return
			}
			chRunNewServer <- newBinaryPath
		})
	})
	return nil
}

func runGolangCILint(ctx context.Context, st *statetrack.Tracker, conf *config.Config) {
	startLinting := time.Now()
	buf, err := cmdrun.Run(
		ctx, conf.App.DirWork, nil,
		"golangci-lint", "run", conf.App.DirSrcRoot+"/...",
	)
	if errors.Is(ctx.Err(), context.Canceled) {
		log.Debugf("golangci-lint cmd aborted")
		return // No need to check errors and continue.
	}
	if errors.Is(err, cmdrun.ErrExitCode1) {
		bufStr := string(buf)
		log.Error(bufStr)
		st.Set(statetrack.IndexGolangciLint, bufStr)
		return
	} else if err != nil {
		log.Errorf("failed running golangci-lint: %v", err)
		return
	}
	st.Set(statetrack.IndexGolangciLint, "")
	log.Durf("linted", time.Since(startLinting))
}

func buildServer(
	ctx context.Context, st *statetrack.Tracker, conf *config.Config, outBasePath string,
) (newBinaryPath string) {
	startBuilding := time.Now()

	binaryPath := makeUniqueServerOutPath(outBasePath)

	args := append([]string{"build"}, conf.CompilerFlags()...)
	args = append(args, "-o", binaryPath, conf.App.DirCmd)
	buf, err := cmdrun.Run(ctx, conf.App.DirWork, conf.CompilerEnv(), "go", args...)
	if errors.Is(ctx.Err(), context.Canceled) {
		log.Debugf("go build cmd aborted")
		return // No need to check errors and continue.
	}
	if err != nil {
		bufStr := string(buf)
		log.Error(bufStr)
		st.Set(statetrack.IndexGo, bufStr)
		return
	}
	// Reset the process exit and go compiler errors
	st.Set(statetrack.IndexGo, "")
	st.Set(statetrack.IndexExit, "")
	log.Durf("compiled cmd/server", time.Since(startBuilding))
	return binaryPath
}

func makeUniqueServerOutPath(basePath string) string {
	tm := time.Now()
	return filepath.Join(basePath, "server_"+strconv.FormatInt(tm.UnixNano(), 16))
}

func lintAndBuildServer(
	ctx context.Context, st *statetrack.Tracker, conf *config.Config, outBasePath string,
) (newBinaryPath string) {
	if st.ErrIndex() == statetrack.IndexTempl {
		return
	}
	var wg sync.WaitGroup
	if conf.Lint {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runGolangCILint(ctx, st, conf)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		newBinaryPath = buildServer(ctx, st, conf, outBasePath)
		log.Debugf("new app server binary: %s", newBinaryPath)
	}()
	wg.Wait() // Wait for build and lint to finish.
	return newBinaryPath
}

func checkTemplVersion(ctx context.Context) error {
	out, err := cmdrun.Run(ctx, "", nil, "templ", "version")
	if err != nil {
		return err
	}
	outStr := strings.TrimSpace(string(out))
	if !strings.HasPrefix(outStr, config.SupportedTemplVersion) {
		log.WarnUnsupportedTemplVersion(
			config.Version, config.SupportedTemplVersion, outStr,
		)
	}
	return nil
}
