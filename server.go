package main

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
)

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
