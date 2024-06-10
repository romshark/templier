package main

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
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
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
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

	workingDir, err := os.Getwd()
	if err != nil {
		panic(fmt.Errorf("getting working dir: %w", err))
	}
	serverOutPath = path.Join(os.TempDir(), workingDir)

	templierBaseURL, err := url.JoinPath("https://", config.TemplierHost)
	if err != nil {
		panic(fmt.Errorf("joining templier base URL: %w", err))
	}

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

	// Watch all files except "*.templ" and the .git subdir
	mustGoWatchDir(
		ctx, config.App.DirSrcRoot,
		config.Debounce.Go, onFileChangedRebuildServer,
		".git",
	)
	// Watch all .templ and regenerate templates
	mustGoWatchDir(
		ctx, config.App.DirSrcRoot,
		config.Debounce.Templ, onTemplFileChangedGenTemplates,
	)

	fmt.Print("🤖 templier ")
	fGreen.Print("started")
	fmt.Print(" on ")
	fBlueUnderline.Println(templierBaseURL)

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
					// successfuly linting & compiling and the templ error
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

func onFileChangedRebuildServer(e fsnotify.Event) {
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

	// Ignore .templ files, another watcher will take care of them.
	if strings.HasSuffix(e.Name, ".templ") {
		return
	}

	fmt.Print("🤖 file ")
	fmt.Print(operation)
	fmt.Print(": ")
	fBlueUnderline.Println(e.Name)

	ctx := context.Background()
	func() {
		if config.Lint && !runGolangCILint(ctx) {
			return
		}
		if !buildAndRerunServer(ctx) {
			return
		}
	}()
}

func onTemplFileChangedGenTemplates(e fsnotify.Event) {
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
	if !strings.HasSuffix(e.Name, ".templ") {
		return
	}

	fmt.Print("🤖 template file ")
	fmt.Print(operation)
	fmt.Print(": ")
	fCyanUnderline.Println(e.Name)

	runTemplGenerate(context.Background(), e.Name)
}

// mustGoWatchDir watches all directories in pathDir ignoring ignoredDirs
// and debounces calls to fn.
func mustGoWatchDir(
	ctx context.Context,
	pathDir string,
	debounceDur time.Duration,
	fn func(e fsnotify.Event),
	ignoredDirs ...string,
) {
	debounce, do := NewDebouncedSync(debounceDur)
	go debounce(ctx)

	forEachDir(pathDir, func(dir string) {
		go func() {
			watcher, err := fsnotify.NewWatcher()
			if err != nil {
				panic(fmt.Errorf("initializing file watcher: %w", err))
			}
			defer watcher.Close()
			if err := watcher.Add(dir); err != nil {
				panic(fmt.Errorf("setting up file watcher: %w", err))
			}
			for {
				select {
				case <-ctx.Done():
					return
				case e := <-watcher.Events:
					do(func() { fn(e) })
				case err := <-watcher.Errors:
					panic(fmt.Errorf("watching file: %w", err))
				}
			}
		}()
	}, ignoredDirs...)
}

// forEachDir executes fn for every subdirectory of pathDir,
// including pathDir itself, recursively.
func forEachDir(pathDir string, fn func(dir string), ignore ...string) {
	// Use filepath.Walk to traverse directories
	err := filepath.Walk(pathDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err // Stop walking the directory tree.
		}
		if !info.IsDir() {
			return nil // Continue walking.
		}
		for _, ignored := range ignore {
			if strings.HasPrefix(path, ignored) {
				return nil
			}
		}
		fn(path)
		return nil
	})
	// Handle potential errors from walking the directory tree.
	if err != nil {
		panic(err) // For simplicity, panic on error. Adjust error handling as needed.
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
	if err := os.MkdirAll(serverOutPath, os.ModePerm); err != nil {
		panic(fmt.Errorf("creating go binary output file path in %q: %w",
			serverOutPath, err))
	}

	binaryPath := makeUniqueServerOutPath(serverOutPath)

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

func makeUniqueServerOutPath(basePath string) string {
	tm := time.Now()
	return path.Join(basePath, "server_"+strconv.FormatInt(tm.UnixNano(), 16))
}

type Server struct {
	httpClient          *http.Client
	appHostAddr         string
	broadcasterRegister chan chan []byte
	jsInjection         []byte
	webSocketUpgrader   websocket.Upgrader
}

func NewServer(
	httpClient *http.Client,
	appHostAddr string,
	printDebugLogs bool,
	broadcasterRegister chan chan []byte,
	connectionRefusedTimeout time.Duration,
) *Server {
	var jsInjectionBuf bytes.Buffer
	err := jsInjection(printDebugLogs, PathProxyEvents).Render(
		context.Background(), &jsInjectionBuf,
	)
	if err != nil {
		panic(fmt.Errorf("rendering the live reload injection template: %w", err))
	}
	return &Server{
		httpClient:          httpClient,
		appHostAddr:         appHostAddr,
		broadcasterRegister: broadcasterRegister,
		jsInjection:         jsInjectionBuf.Bytes(),
		webSocketUpgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true }, // Ignore CORS
		},
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == PathProxyEvents {
		// This request comes from the injected JavaScript,
		// Don't forward, handle it instead.
		s.handleProxyEvents(w, r)
		return
	}

	state := currentState.Load().(State)
	if state.Type.IsErr() {
		s.handleErrPage(w, r)
		return
	}

	u, err := url.JoinPath(s.appHostAddr, r.URL.Path)
	if err != nil {
		internalErr(w, "joining path", err)
		return
	}

	proxyReq, err := http.NewRequestWithContext(
		r.Context(), r.Method, u, r.Body,
	)
	if err != nil {
		internalErr(w, "initializing request", err)
		return
	}

	// Copy original request headers
	proxyReq.Header = r.Header.Clone()

	var resp *http.Response
	for start := time.Now(); ; {
		resp, err = s.httpClient.Do(proxyReq)
		if err != nil {
			if isConnRefused(err) {
				if time.Since(start) < config.ProxyTimeout {
					continue
				}
			}
			http.Error(w,
				fmt.Sprintf("proxy: sending request: %v", err),
				http.StatusInternalServerError)
			return
		}
		break
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Check if the response content type is HTML to inject the script
	if resp.StatusCode == http.StatusOK &&
		strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			internalErr(w, "reading response body", err)
			return
		}
		// Inject JavaScript
		modified := bytes.Replace(b, bytesBodyClosingTag, s.jsInjection, 1)
		w.Header().Set("Content-Length", strconv.Itoa(len(modified)))
		_, _ = w.Write(modified)

	} else {
		// For non-HTML responses, just proxy the response
		_, _ = io.Copy(w, resp.Body)
		w.WriteHeader(resp.StatusCode)
	}
}

func isConnRefused(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		const c = syscall.ECONNREFUSED
		if sysErr, ok := opErr.Err.(*os.SyscallError); ok && sysErr.Err == c {
			return true
		}
	}
	return false
}

func (s *Server) handleProxyEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w,
			"expecting method GET on templier proxy route 'events'",
			http.StatusMethodNotAllowed)
		return
	}
	c, err := s.webSocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("🤖 ERR: upgrading to websocket:", err)
		internalErr(w, "upgrading to websocket", err)
		return
	}
	defer c.Close()

	messages := make(chan []byte)
	s.broadcasterRegister <- messages

	for msg := range messages {
		err = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err != nil {
			fmt.Println("🤖 ERR: setting websocket write deadline:", err)
		}
		err = c.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			return // Disconnected
		}
	}
}

func (s *Server) handleErrPage(w http.ResponseWriter, r *http.Request) {
	state := currentState.Load().(State)

	var header string
	switch state.Type {
	case StateTypeErrTempl:
		header = "Error: Templ"
	case StateTypeErrCompile:
		header = "Error: Compiling"
	case StateTypeErrGolangCILint:
		header = "Error: golangci-lint"
	default:
		header = "Error"
	}
	title := header

	comp := errpage(
		title, header, string(state.Msg),
		config.PrintJSDebugLogs, PathProxyEvents,
	)
	err := comp.Render(r.Context(), w)
	if err != nil {
		panic(fmt.Errorf("rendering errpage: %w", err))
	}
}

var bytesBodyClosingTag = []byte("</body>")

func internalErr(w http.ResponseWriter, msg string, err error) {
	http.Error(w,
		fmt.Sprintf("proxy: %s: %v", msg, err),
		http.StatusInternalServerError)
}
