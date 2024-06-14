package main

import (
	"context"
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
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

	"github.com/romshark/templier/internal/debounce"
	"github.com/romshark/templier/internal/watcher"

	"github.com/fatih/color"
	"github.com/fsnotify/fsnotify"
)

const (
	ServerHealthPreflightWaitInterval = 100 * time.Millisecond

	// PathProxyEvents defines the path for templier proxy websocket events endpoint.
	// This path is very unlikely to collide with any path used by the app server.
	PathProxyEvents = "/🔌💥"
)

type StateType int8

const (
	StateTypeOK StateType = iota
	StateTypeErrTempl
	StateTypeErrCompile
	StateTypeErrGolangCILint
)

func (s StateType) IsErr() bool {
	switch s {
	case StateTypeErrTempl:
		return true
	case StateTypeErrCompile:
		return true
	case StateTypeErrGolangCILint:
		return true
	}
	return false
}

type State struct {
	Type StateType
	Msg  string
}

var (
	chState              = make(chan State)
	chMsgClients         = make(chan []byte)
	chBrodcasterRegister = make(chan chan []byte, 1)
	chBrodcasterDelete   = make(chan chan []byte, 1)
	chRerunServer        = make(chan string, 1)
	chStopServer         = make(chan struct{})

	// filesToBeDeletedBeforeExit keeps a path->struct{} register to make sure
	// all files created by this process are defer-deleted.
	filesToBeDeletedBeforeExit = NewSyncStringSet()
)

var (
	fBlueUnderline = color.New(color.FgBlue, color.Underline)
	fGreen         = color.New(color.FgGreen, color.Bold)
	fCyanUnderline = color.New(color.FgCyan, color.Underline)
	fRed           = color.New(color.FgHiRed, color.Bold)
)

func main() {
	mustParseConfig()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	defer func() { // Make sure files created by this process are cleaned up
		fmt.Println("🤖 cleaning up all created files")
		filesToBeDeletedBeforeExit.ForEach(func(filePath string) {
			if err := os.RemoveAll(filePath); err != nil {
				fmt.Printf("🤖 ERR: removing (%q): %v", filePath, err)
			}
		})
	}()

	go runStateTracker()

	func() {
		if !generateAllTemplates(ctx) {
			return
		}
		if config.Lint && !runGolangCILint(ctx) {
			return
		}
		if !buildAndRerunServer(ctx) {
			return
		}
	}()

	go runTemplierServer()
	go runAppLauncher()

	debouncerTempl, debouncedTempl := debounce.NewSync(config.Debounce.Templ)
	go debouncerTempl(ctx)

	debouncerGo, debouncedGo := debounce.NewSync(config.Debounce.Go)
	go debouncerGo(ctx)

	watcher, err := watcher.New(func(ctx context.Context, e fsnotify.Event) {
		debounce := debouncedGo
		if isTemplFile(e.Name) {
			// Use different debouncer for .templ files
			debounce = debouncedTempl
		}
		debounce(func() { onFileChanged(ctx, e) })
	})
	if err != nil {
		fmt.Printf("🤖 ERR: initializing file watcher: %v", err)
		os.Exit(1)
	}

	go func() {
		if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			panic(fmt.Errorf("running file watcher:%w", err))
		}
	}()

	fmt.Println("ADD", config.App.dirSrcRootAbsolute)
	if err := watcher.Add(config.App.dirSrcRootAbsolute); err != nil {
		fmt.Printf("🤖 ERR: setting up file watcher for app.dir-src-root(%q): %v",
			config.App.dirSrcRootAbsolute, err)
		os.Exit(1)
	}

	{
		templierBaseURL := url.URL{
			Scheme: "http",
			Host:   config.TemplierHost,
		}
		if config.TLS != nil {
			templierBaseURL.Scheme = "https"
		}

		fmt.Print("🤖 templier ")
		fGreen.Print("started")
		fmt.Print(" on ")
		fBlueUnderline.Println(templierBaseURL.String())
	}

	<-ctx.Done()
	chStopServer <- struct{}{}
}

var currentState atomic.Value

func init() {
	currentState.Store(State{Type: StateTypeOK})
}

// runStateTracker tracks the current state of the env and broadcasts updates.
func runStateTracker() {
	m := make(map[chan []byte]struct{})
	for {
		select {
		case ch := <-chBrodcasterRegister:
			m[ch] = struct{}{}
		case ch := <-chBrodcasterDelete:
			close(ch)
			delete(m, ch)
		case msg := <-chMsgClients:
			for ch := range m {
				select {
				case ch <- msg:
				default: // Ignore unresponsive listeners
				}
			}
		case newState := <-chState:
			state := currentState.Load().(State)

			if state.Type == StateTypeErrTempl {
				switch newState.Type {
				case StateTypeErrTempl, StateTypeOK:
					// It's okay to overwrite templ error with a new
					// templ error or success.
				default:
					// Don't overwrite ErrTempl with other errors
					// because even though templ failed it could continue
					// successfully linting & compiling and the templ error
					// would be overwritten.
					return
				}
			}
			currentState.Store(newState)
			for ch := range m {
				select {
				case ch <- bytesMsgReload:
				default:
					// Ignore unresponsive listeners
				}
			}
		}
	}
}

var (
	bytesMsgReload          = []byte("r")
	bytesMsgReloadInitiated = []byte("ri")
)

func runTemplierServer() {
	srv := NewServer(
		&http.Client{
			Timeout: config.ProxyTimeout,
		},
		config.App.Host,
		config.PrintJSDebugLogs,
		chBrodcasterRegister,
		config.ProxyTimeout,
	)
	var err error
	if config.TLS != nil {
		err = http.ListenAndServeTLS(config.TemplierHost,
			config.TLS.Cert, config.TLS.Key, srv)
	} else {
		err = http.ListenAndServe(config.TemplierHost, srv)
	}
	if err != nil {
		panic(fmt.Errorf("listening templier host: %w", err))
	}
}

func runAppLauncher() {
	var latestSrvCmd *exec.Cmd
	var latestBinaryPath string

	stopServer := func() (ok bool) {
		if latestSrvCmd == nil || latestSrvCmd.Process == nil {
			return true
		}
		if err := latestSrvCmd.Process.Signal(os.Interrupt); err != nil {
			fRed.Print("🤖 sending interrupt signal to cmd/server: ")
			fRed.Println(err.Error())
			return false
		}
		if _, err := latestSrvCmd.Process.Wait(); err != nil {
			fRed.Printf("🤖 waiting for cmd/server (%d) to terminate: ",
				latestSrvCmd.Process.Pid)
			fRed.Println(err.Error())
			return false
		}
		if err := os.Remove(latestBinaryPath); err != nil {
			fRed.Printf("🤖 removing binary file %q: ", latestBinaryPath)
			fRed.Println(err.Error())
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
			stopServer()
			latestBinaryPath = binaryPath
			c := exec.Command(binaryPath)
			c.Args = config.App.Flags
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			latestSrvCmd = c
			fmt.Print("🤖 restarting ")
			fGreen.Print("cmd/server")
			fmt.Println("...")
			if err := c.Start(); err != nil {
				fRed.Print("🤖 running cmd/server: ")
				fRed.Println(err.Error())
			}
			const maxRetries = 100
			for retry := 0; ; retry++ {
				if retry > maxRetries {
					fRed.Printf("🤖 waiting for server: %d retries failed\n", maxRetries)
					continue AWAIT_COMMAND
				}
				// Wait for the server to be ready
				r, err := http.NewRequest(
					http.MethodOptions, config.App.Host, http.NoBody,
				)
				if err != nil {
					fmt.Println("🤖 ERR: initializing preflight request:", err)
				}
				_, err = healtCheckClient.Do(r)
				if err == nil {
					break // Server is ready to receive requests
				}
				time.Sleep(ServerHealthPreflightWaitInterval)
			}
			// Notify all clients to reload the page
			chState <- State{Type: StateTypeOK} // OK
		case <-chStopServer:
			stopServer()
		}
	}
}

// fileChangedLock prevents more than one rebuilder goroutine at a time.
var fileChangedLock sync.Mutex

func onFileChanged(ctx context.Context, e fsnotify.Event) {
	switch {
	case isTemplFile(e.Name):
		fileChangedLock.Lock()
		defer fileChangedLock.Unlock()

		var operation string
		switch e.Op {
		case fsnotify.Create:
			operation = "created"
		case fsnotify.Write:
			operation = "changed"
		case fsnotify.Remove:
			operation = "removed"
		default:
			return
		}

		fmt.Print("🤖 template file ")
		fmt.Print(operation)
		fmt.Print(": ")
		fCyanUnderline.Println(e.Name)

		runTemplGenerate(ctx, e.Name)

	default:
		switch currentState.Load().(State).Type {
		case StateTypeErrTempl, StateTypeErrGolangCILint:
			return
		}

		fileChangedLock.Lock()
		defer fileChangedLock.Unlock()

		chMsgClients <- bytesMsgReloadInitiated

		var operation string
		switch e.Op {
		case fsnotify.Create:
			operation = "created"
		case fsnotify.Write:
			operation = "changed"
		case fsnotify.Remove:
			operation = "removed"
		default:
			return
		}

		fmt.Print("🤖 file ")
		fmt.Print(operation)
		fmt.Print(": ")
		fBlueUnderline.Println(e.Name)

		func() {
			if config.Lint && !runGolangCILint(ctx) {
				return
			}
			if !buildAndRerunServer(ctx) {
				return
			}
		}()
	}
}

func generateAllTemplates(ctx context.Context) (ok bool) {
	c := exec.CommandContext(ctx, "templ", "generate", config.App.DirSrcRoot)
	if buf, err := c.CombinedOutput(); err != nil {
		fRed.Printf("generating all templates in '%s': ", config.App.DirSrcRoot)
		chState <- State{Type: StateTypeErrTempl, Msg: string(buf)}
		return false
	}
	return true
}

func runTemplGenerate(ctx context.Context, filePath string) (ok bool) {
	c := exec.CommandContext(ctx, "templ", "generate", filePath)
	if buf, err := c.CombinedOutput(); err != nil {
		fRed.Printf("generating '%s': ", filePath)
		chState <- State{Type: StateTypeErrTempl, Msg: string(buf)}
		return false
	}
	return true
}

func runGolangCILint(ctx context.Context) (ok bool) {
	c := exec.CommandContext(ctx, "golangci-lint", "run", config.App.DirSrcRoot+"/...")
	if buf, err := c.CombinedOutput(); err != nil {
		fRed.Println("🤖 failed running golangci-lint:", err)
		chState <- State{Type: StateTypeErrGolangCILint, Msg: string(buf)}
		return false
	}
	return true
}

func buildAndRerunServer(ctx context.Context) (ok bool) {
	if err := os.MkdirAll(config.serverOutPath, os.ModePerm); err != nil {
		panic(fmt.Errorf("creating go binary output file path in %q: %w",
			config.serverOutPath, err))
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
		fRed.Println("🤖 failed compiling cmd/server")
		chState <- State{Type: StateTypeErrCompile, Msg: string(buf)}
		return false
	}
	chRerunServer <- binaryPath
	return true
}

func isTemplFile(filePath string) bool {
	return strings.HasSuffix(filePath, ".templ")
}

func makeUniqueServerOutPath(basePath string) string {
	tm := time.Now()
	return path.Join(basePath, "server_"+strconv.FormatInt(tm.UnixNano(), 16))
}
