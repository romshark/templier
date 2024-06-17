package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/romshark/templier/internal/broadcaster"
	"github.com/romshark/templier/internal/state"
)

// PathProxyEvents defines the path for templier proxy websocket events endpoint.
// This path is very unlikely to collide with any path used by the app server.
const PathProxyEvents = "/_templiér/events"

type Config struct {
	PrintJSDebugLogs bool
	AppHostAddr      string
	ProxyTimeout     time.Duration
}

type Server struct {
	config            Config
	httpClient        *http.Client
	stateTracker      *state.Tracker
	reloadInitiated   *broadcaster.SignalBroadcaster
	reload            *broadcaster.SignalBroadcaster
	jsInjection       []byte
	webSocketUpgrader websocket.Upgrader
}

func New(
	httpClient *http.Client,
	stateTracker *state.Tracker,
	reloadInitiated *broadcaster.SignalBroadcaster,
	reload *broadcaster.SignalBroadcaster,
	config Config,
) *Server {
	var jsInjectionBuf bytes.Buffer
	err := jsInjection(config.PrintJSDebugLogs, PathProxyEvents).Render(
		context.Background(), &jsInjectionBuf,
	)
	if err != nil {
		panic(fmt.Errorf("rendering the live reload injection template: %w", err))
	}
	return &Server{
		config:          config,
		httpClient:      httpClient,
		stateTracker:    stateTracker,
		reloadInitiated: reloadInitiated,
		reload:          reload,
		jsInjection:     jsInjectionBuf.Bytes(),
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

	if state := s.stateTracker.Get(); state.IsErr() {
		s.handleErrPage(w, r)
		return
	}

	u, err := url.JoinPath(s.config.AppHostAddr, r.URL.Path)
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
				if time.Since(start) < s.config.ProxyTimeout {
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
		if _, err = io.Copy(w, resp.Body); err != nil {
			internalErr(w, "copying response body", err)
			return
		}
		if resp.StatusCode != http.StatusOK {
			w.WriteHeader(resp.StatusCode)
		}
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

	notifyStateChange := make(chan struct{})
	s.stateTracker.AddListener(notifyStateChange)
	defer s.stateTracker.RemoveListener(notifyStateChange)

	notifyReloadInitiated := make(chan struct{})
	s.reloadInitiated.AddListener(notifyReloadInitiated)
	defer s.reloadInitiated.RemoveListener(notifyReloadInitiated)

	notifyReload := make(chan struct{})
	s.reload.AddListener(notifyReload)
	defer s.reload.RemoveListener(notifyReload)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-notifyStateChange:
			if !writeWSMsg(c, bytesMsgReload) {
				return // Disconnect
			}
		case <-notifyReloadInitiated:
			if !writeWSMsg(c, bytesMsgReloadInitiated) {
				return // Disconnect
			}
		case <-notifyReload:
			if !writeWSMsg(c, bytesMsgReload) {
				return // Disconnect
			}
		}
	}
}

func writeWSMsg(c *websocket.Conn, msg []byte) (ok bool) {
	err := c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err != nil {
		return false
	}
	err = c.WriteMessage(websocket.TextMessage, msg)
	return err == nil
}

var (
	bytesMsgReload          = []byte("r")
	bytesMsgReloadInitiated = []byte("ri")
)

func (s *Server) handleErrPage(w http.ResponseWriter, r *http.Request) {
	state := s.stateTracker.Get()

	var header string
	switch {
	case state.ErrTempl != "":
		header = "Error: Templ"
	case state.ErrGo != "":
		header = "Error: Compiling"
	case state.ErrGolangCILint != "":
		header = "Error: golangci-lint"
	default:
		header = "Error"
	}
	title := header

	comp := errpage(
		title, header, string(state.Msg()),
		s.config.PrintJSDebugLogs, PathProxyEvents,
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
