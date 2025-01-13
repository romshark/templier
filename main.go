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
	"path"
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
	"github.com/romshark/templier/internal/debounce"
	"github.com/romshark/templier/internal/log"
	"github.com/romshark/templier/internal/server"
	"github.com/romshark/templier/internal/statetrack"
	"github.com/romshark/templier/internal/syncstrset"
	"github.com/romshark/templier/internal/watcher"
	"golang.org/x/sync/errgroup"

	"github.com/fsnotify/fsnotify"
	"github.com/gobwas/glob"
)

const ServerHealthPreflightWaitInterval = 100 * time.Millisecond

var (
	chRerunServer  = make(chan struct{}, 1)
	chRunNewServer = make(chan string, 1)

	// filesToBeDeletedBeforeExit keeps a path->struct{} register to make sure
	// all files created by this process are defer-deleted.
	filesToBeDeletedBeforeExit = syncstrset.New()
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

	defer func() { // Make sure files created by this process are cleaned up
		log.Infof("cleaning up all created files")
		filesToBeDeletedBeforeExit.ForEach(func(filePath string) {
			log.Debugf("removing file before exit: %q", filePath)
			if err := os.RemoveAll(filePath); err != nil {
				log.Errorf("removing (%q): %v", filePath, err)
			}
		})
	}()

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

	debouncerTempl, debouncedTempl := debounce.NewSync(conf.Debounce.Templ)
	go debouncerTempl(ctx)

	debouncer, debounced := debounce.NewSync(conf.Debounce.Go)
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
	if binaryFile := lintAndBuildServer(ctx, st, conf); binaryFile != "" {
		// Launch only when there's no errors on initial build.
		chRunNewServer <- binaryFile
	}

	onChangeHandler := FileChangeHandler{
		customWatchers:          customWatchers,
		stateTracker:            st,
		reload:                  reload,
		debouncedNonTempl:       debounced,
		debouncedTemplTxtChange: debouncedTempl,
		conf:                    conf,
		ignoreWriteChangeByPath: map[string]struct{}{},
	}

	var err error
	onChangeHandler.baseFilePath, err = filepath.Abs(conf.App.DirWork)
	if err != nil {
		log.Fatalf("determining absolute base file path: %v", err)
	}
	log.Debugf("set absolute base file path: %q", onChangeHandler.baseFilePath)

	watcher, err := watcher.New(conf.App.DirSrcRootAbsolute(), onChangeHandler.Handle)
	if err != nil {
		log.Fatalf("initializing file watcher: %v", err)
	}

	for _, expr := range conf.App.Exclude {
		if err := watcher.Ignore(expr); err != nil {
			log.Fatalf("adding ignore filter to watcher (%q): %v", expr, err)
		}
	}

	// Ignore templ temp files generated by `templ fmt`.
	if err := watcher.Ignore("*.templ[0-9]*"); err != nil {
		log.Fatalf(
			`adding ignore templ temp files filter to watcher ("*.templ*"): %v`,
			err,
		)
	}

	if err := watcher.Add(conf.App.DirSrcRootAbsolute()); err != nil {
		log.Fatalf("setting up file watcher for app.dir-src-root(%q): %v",
			conf.App.DirSrcRootAbsolute(), err)
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
	cancel() // Ask all sub-processes to exit gracefuly.
	log.Debugf("waiting for remaining sub-processes to shut down")
	wg.Wait() // Wait for all sub-processes to exit.
}

// rebuildLock prevents more than one rebuilder goroutine at a time.
var rebuildLock sync.Mutex

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
		log.Debugf("app server running (pid: %d)", c.Process.Pid)

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
			_, err = healthCheckClient.Do(r)
			if err == nil {
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
				if err := os.Remove(latestBinaryPath); err != nil {
					log.Errorf("removing binary file %q: %v", latestBinaryPath, err)
				}
				filesToBeDeletedBeforeExit.Delete(latestBinaryPath)
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
	baseFilePath            string
	customWatchers          []customWatcher
	stateTracker            *statetrack.Tracker
	reload                  *broadcaster.SignalBroadcaster
	debouncedNonTempl       func(fn func())
	debouncedTemplTxtChange func(fn func())
	conf                    *config.Config
	ignoreWriteChangeByPath map[string]struct{}
}

func (h *FileChangeHandler) Handle(ctx context.Context, e fsnotify.Event) error {
	if e.Op == fsnotify.Chmod {
		log.Debugf("ignoring file operation (%s): %q", e.Op.String(), e.Name)
		return nil // Ignore chmod events.
	}

	if _, ok := h.ignoreWriteChangeByPath[e.Name]; ok {
		log.Debugf(
			"ignoring _templ.go file operation after .templ update (%s): %q",
			e.Op.String(), e.Name,
		)
		delete(h.ignoreWriteChangeByPath, e.Name)
		return nil // Ignore this change.
	}

	if h.conf.Log.ClearOn == config.LogClearOnFileChange {
		log.ClearLogs()
	}

	relativeFileName, err := filepath.Rel(h.baseFilePath, e.Name)
	if err != nil {
		log.Fatalf(
			"determining relative path for %q with base path %q",
			e.Name, h.conf.App.DirWork,
		)
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
		// No custom watcher triggered, follow default pipeline.
		if before, found := strings.CutSuffix(e.Name, ".templ"); found {
			// Ignore the next _templ.go update for this file because we're running
			// templ in dev mode and since templ v0.3.819 the generated _templ.go file
			// is identical for both dev and prod builds. Templ will update both the
			// _templ.txt and the _templ.go files and we just need to make sure
			// the server compiles without reloading the page.
			h.ignoreWriteChangeByPath[before+"_templ.go"] = struct{}{}

			binaryPath := lintAndBuildServer(ctx, h.stateTracker, h.conf)
			if err := os.Remove(binaryPath); err != nil {
				log.Debugf("removing the temporary server binary: %v", err)
			}
			compiler := h.stateTracker.Get(statetrack.IndexGo)
			linter := h.stateTracker.Get(statetrack.IndexGolangciLint)
			if compiler != "" || linter != "" {
				h.reload.BroadcastNonblock()
			}
			return nil
		}
		if h.stateTracker.Get(statetrack.IndexTempl) != "" {
			// A templ template is broken, don't continue.
			return nil
		}
		if strings.HasSuffix(e.Name, "_templ.txt") {
			// Reload browser tabs when a _templ.txt file has changed.
			h.debouncedTemplTxtChange(func() {
				h.reload.BroadcastNonblock()
			})
			return nil
		}
	}

	h.debouncedNonTempl(func() {
		rebuildLock.Lock()
		defer rebuildLock.Unlock()

		// templ files are OK, a non-templ file was changed.
		log.TemplierFileChange(e)

		newBinaryPath := lintAndBuildServer(ctx, h.stateTracker, h.conf)
		if h.stateTracker.ErrIndex() != -1 {
			h.reload.BroadcastNonblock()
			// Don't restart the server if there was any error.
			return
		}
		chRunNewServer <- newBinaryPath
	})
	return nil
}

func runGolangCILint(ctx context.Context, st *statetrack.Tracker, conf *config.Config) {
	startLinting := time.Now()
	buf, err := cmdrun.Run(
		ctx, conf.App.DirWork, nil,
		"golangci-lint", "run", conf.App.DirSrcRoot+"/...",
	)
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
	ctx context.Context, st *statetrack.Tracker, conf *config.Config,
) (newBinaryPath string) {
	startBuilding := time.Now()
	if err := os.MkdirAll(conf.ServerOutPath(), os.ModePerm); err != nil {
		log.Errorf("creating go binary output file path in %q: %v",
			conf.ServerOutPath(), err)
		st.Set(statetrack.IndexGo, err.Error())
		return
	}

	binaryPath := makeUniqueServerOutPath(conf.ServerOutPath())

	// Register the binary path to make sure it's defer-deleted
	filesToBeDeletedBeforeExit.Store(binaryPath)

	args := append([]string{"build"}, conf.CompilerFlags()...)
	args = append(args, "-o", binaryPath, conf.App.DirCmd)
	buf, err := cmdrun.Run(ctx, conf.App.DirWork, conf.CompilerEnv(), "go", args...)
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
	return path.Join(basePath, "server_"+strconv.FormatInt(tm.UnixNano(), 16))
}

func lintAndBuildServer(
	ctx context.Context, st *statetrack.Tracker, conf *config.Config,
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
		newBinaryPath = buildServer(ctx, st, conf)
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
