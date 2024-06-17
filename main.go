package main

import (
	"context"
	"crypto/tls"
	_ "embed"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/romshark/templier/internal/broadcaster"
	"github.com/romshark/templier/internal/debounce"
	"github.com/romshark/templier/internal/log"
	"github.com/romshark/templier/internal/server"
	"github.com/romshark/templier/internal/state"
	"github.com/romshark/templier/internal/syncstrset"
	"github.com/romshark/templier/internal/templrun"
	"github.com/romshark/templier/internal/watcher"

	"github.com/fsnotify/fsnotify"
)

const ServerHealthPreflightWaitInterval = 100 * time.Millisecond

var (
	chRerunServer = make(chan string, 1)
	chStopServer  = make(chan struct{})

	// filesToBeDeletedBeforeExit keeps a path->struct{} register to make sure
	// all files created by this process are defer-deleted.
	filesToBeDeletedBeforeExit = syncstrset.New()
)

func main() {
	mustParseConfig()
	log.SetVerbose(config.Verbose)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	defer func() { // Make sure files created by this process are cleaned up
		log.Infof("cleaning up all created files")
		filesToBeDeletedBeforeExit.ForEach(func(filePath string) {
			if err := os.RemoveAll(filePath); err != nil {
				log.Errorf("removing (%q): %v", filePath, err)
			}
		})
	}()

	reloadInitiated := broadcaster.NewSignalBroadcaster()
	reload := broadcaster.NewSignalBroadcaster()
	st := state.NewTracker()

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		// Run templ in watch mode to create debug components.
		// Once ctx is canceled templ will write production output and exit.
		err := templrun.RunWatch(ctx, config.App.dirSrcRootAbsolute, st)
		if err != nil {
			log.Errorf("running 'templ generate --watch': %v", err)
		}
	}()

	func() { // Initial build
		if config.Lint {
			go runGolangCILint(ctx, st)
		}
		go buildAndRerunServer(ctx, st)
	}()

	go runTemplierServer(st, reloadInitiated, reload)

	rerunTriggerStart.Store(time.Now())
	go runAppLauncher(st)

	debouncerTempl, debouncedTempl := debounce.NewSync(config.Debounce.Templ)
	go debouncerTempl(ctx)

	debouncer, debounced := debounce.NewSync(config.Debounce.Go)
	go debouncer(ctx)

	onChangeHandler := FileChangeHandler{
		stateTracker:            st,
		reload:                  reload,
		reloadInitiated:         reloadInitiated,
		debouncedNonTempl:       debounced,
		debouncedTemplTxtChange: debouncedTempl,
	}
	watcher, err := watcher.New(config.App.dirSrcRootAbsolute, onChangeHandler.Handle)
	if err != nil {
		log.Fatalf("initializing file watcher: %v", err)
	}

	go func() {
		if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("running file watcher: %v", err)
		}
	}()

	for _, expr := range config.App.Exclude {
		if err := watcher.Ignore(expr); err != nil {
			log.Fatalf("adding ignore filter to watcher (%q): %v", expr, err)
		}
	}

	if err := watcher.Add(config.App.dirSrcRootAbsolute); err != nil {
		log.Fatalf("setting up file watcher for app.dir-src-root(%q): %v",
			config.App.dirSrcRootAbsolute, err)
	}

	{
		templierBaseURL := url.URL{
			Scheme: "http",
			Host:   config.TemplierHost,
		}
		if config.TLS != nil {
			templierBaseURL.Scheme = "https"
		}

		log.TemplierStarted(templierBaseURL.String())
	}

	<-ctx.Done()
	wg.Wait()
	chStopServer <- struct{}{}
}

var (
	// fileChangedLock prevents more than one rebuilder goroutine at a time.
	fileChangedLock   sync.Mutex
	rerunTriggerStart atomic.Value
)

func runTemplierServer(
	st *state.Tracker, reloadInitiated, reload *broadcaster.SignalBroadcaster,
) {
	srv := server.New(
		&http.Client{
			Timeout: config.ProxyTimeout,
		},
		st,
		reloadInitiated,
		reload,
		server.Config{
			AppHostAddr:      config.App.Host,
			PrintJSDebugLogs: config.PrintJSDebugLogs,
			ProxyTimeout:     config.ProxyTimeout,
		},
	)
	var err error
	if config.TLS != nil {
		err = http.ListenAndServeTLS(config.TemplierHost,
			config.TLS.Cert, config.TLS.Key, srv)
	} else {
		err = http.ListenAndServe(config.TemplierHost, srv)
	}
	if err != nil {
		log.Fatalf("listening templier host: %v", err)
	}
}

func runAppLauncher(stateTracker *state.Tracker) {
	var latestSrvCmd *exec.Cmd
	var latestBinaryPath string

	stopServer := func() (ok bool) {
		if latestSrvCmd == nil || latestSrvCmd.Process == nil {
			return true
		}
		if err := latestSrvCmd.Process.Signal(os.Interrupt); err != nil {
			log.Errorf("sending interrupt signal to app server: %v", err)
			return false
		}
		if _, err := latestSrvCmd.Process.Wait(); err != nil {
			log.Errorf("waiting for app server (pid: %d) to terminate: %v",
				latestSrvCmd.Process.Pid, err)
			return false
		}
		if err := os.Remove(latestBinaryPath); err != nil {
			log.Errorf("removing binary file %q: %v", latestBinaryPath, err)
			return false
		}
		filesToBeDeletedBeforeExit.Delete(latestBinaryPath)
		return true
	}

	healtCheckClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}

AWAIT_COMMAND:
	for {
		select {
		case binaryPath := <-chRerunServer:
			start := time.Now()
			stopServer()

			log.Durf("stopped server", time.Since(start))

			start = time.Now()
			latestBinaryPath = binaryPath
			c := exec.Command(binaryPath)
			c.Args = config.App.Flags
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			latestSrvCmd = c

			log.TemplierRestartingServer(config.App.DirCmd)

			if err := c.Start(); err != nil {
				log.Errorf("running %s: %v", config.App.DirCmd, err)
			}
			const maxRetries = 100
			for retry := 0; ; retry++ {
				if retry > maxRetries {
					log.Errorf("waiting for server: %d retries failed", maxRetries)
					continue AWAIT_COMMAND
				}
				// Wait for the server to be ready
				r, err := http.NewRequest(
					http.MethodOptions, config.App.Host, http.NoBody,
				)
				if err != nil {
					log.Errorf("initializing preflight request: %v", err)
					continue
				}
				_, err = healtCheckClient.Do(r)
				if err == nil {
					break // Server is ready to receive requests
				}
				time.Sleep(ServerHealthPreflightWaitInterval)
			}

			log.Durf("restarted server", time.Since(start))
			rerunStart := rerunTriggerStart.Load().(time.Time)
			log.Durf("reloaded", time.Since(rerunStart))

			// Notify all clients to reload the page
			stateTracker.Reset()
		case <-chStopServer:
			stopServer()
		}
	}
}

type FileChangeHandler struct {
	stateTracker            *state.Tracker
	reload                  *broadcaster.SignalBroadcaster
	reloadInitiated         *broadcaster.SignalBroadcaster
	debouncedNonTempl       func(fn func())
	debouncedTemplTxtChange func(fn func())
}

func (h *FileChangeHandler) Handle(ctx context.Context, e fsnotify.Event) {
	if e.Op == fsnotify.Chmod {
		return // Ignore chmod events.
	}
	if h.stateTracker.Get().ErrTempl != "" {
		// A templ template is broken, don't continue.
		return
	}
	if strings.HasSuffix(e.Name, "_templ.txt") {
		// Reload browser tabs when a _templ.txt file has changed.
		h.debouncedTemplTxtChange(func() {
			h.reload.BroadcastNonblock()
		})
		return
	}
	if strings.HasSuffix(e.Name, "_templ.go") ||
		strings.HasSuffix(e.Name, ".templ") {
		return // Ignore templ files, templ watch will take care of them.
	}

	h.debouncedNonTempl(func() {
		fileChangedLock.Lock()
		defer fileChangedLock.Unlock()

		// templ files are OK, a non-templ file was changed.
		// Notify all clients that a reload has been initiated
		// and try to rebuild the server.
		h.reloadInitiated.BroadcastNonblock()

		log.TemplierFileChange(e)

		func() {
			rerunTriggerStart.Store(time.Now())
			if config.Lint {
				go runGolangCILint(ctx, h.stateTracker)
			}
			go buildAndRerunServer(ctx, h.stateTracker)
		}()
	})
}

func runGolangCILint(ctx context.Context, st *state.Tracker) {
	startLinting := time.Now()
	c := exec.CommandContext(ctx, "golangci-lint", "run", config.App.DirSrcRoot+"/...")
	if buf, err := c.CombinedOutput(); err != nil {
		log.Errorf("failed running golangci-lint: %v", err)
		st.SetErrGolangCILint(string(buf))
		return
	}
	log.Durf("linted", time.Since(startLinting))
}

func buildAndRerunServer(ctx context.Context, st *state.Tracker) {
	startBuilding := time.Now()
	if err := os.MkdirAll(config.serverOutPath, os.ModePerm); err != nil {
		log.Errorf("creating go binary output file path in %q: %v",
			config.serverOutPath, err)
		st.SetErrGo(err.Error())
		return
	}

	binaryPath := makeUniqueServerOutPath(config.serverOutPath)

	// Register the binary path to make sure it's defer-deleted
	filesToBeDeletedBeforeExit.Store(binaryPath)

	args := append(
		[]string{"build", "-o", binaryPath, config.App.DirCmd},
		config.App.GoFlags...,
	)
	c := exec.CommandContext(ctx, "go", args...)
	c.Dir = config.App.DirWork
	if buf, err := c.CombinedOutput(); err != nil {
		log.Errorf("failed compiling cmd/server")
		st.SetErrGo(string(buf))
		return
	}
	log.Durf("compiled cmd/server", time.Since(startBuilding))
	chRerunServer <- binaryPath
}

func makeUniqueServerOutPath(basePath string) string {
	tm := time.Now()
	return path.Join(basePath, "server_"+strconv.FormatInt(tm.UnixNano(), 16))
}
