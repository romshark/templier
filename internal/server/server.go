package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gorilla/websocket"
	"github.com/romshark/templier/internal/broadcaster"
	"github.com/romshark/templier/internal/state"
)

const (
	HeaderHXRequest       = "HX-Request"
	HeaderTemplSkipModify = "templ-skip-modify"

	// PathProxyEvents defines the path for templier proxy websocket events endpoint.
	// This path is very unlikely to collide with any path used by the app server.
	PathProxyEvents = "/_templiér/events"

	ReverseProxyRetries         = 20
	ReverseProxyInitialDelay    = 100 * time.Millisecond
	ReverseProxyBackoffExponent = 1.5
)

type Config struct {
	PrintJSDebugLogs bool
	AppHost          *url.URL
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
	reverseProxy      *httputil.ReverseProxy
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
	_, _ = jsInjectionBuf.Write(bytesBodyClosingTag)
	s := &Server{
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
	s.reverseProxy = httputil.NewSingleHostReverseProxy(config.AppHost)
	s.reverseProxy.Transport = &roundTripper{
		maxRetries:      ReverseProxyRetries,
		initialDelay:    ReverseProxyInitialDelay,
		backoffExponent: ReverseProxyBackoffExponent,
	}
	s.reverseProxy.ModifyResponse = s.modifyResponse
	return s
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
	s.reverseProxy.ServeHTTP(w, r)
}

func (s *Server) modifyResponse(r *http.Response) error {
	if r.Header.Get(HeaderTemplSkipModify) == "true" {
		return nil
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		return nil
	}

	// Set up readers and writers.
	newReader := func(in io.Reader) (out io.Reader, err error) {
		return in, nil
	}
	newWriter := func(out io.Writer) io.WriteCloser {
		return passthroughWriteCloser{out}
	}
	switch r.Header.Get("Content-Encoding") {
	case "gzip":
		newReader = func(in io.Reader) (out io.Reader, err error) {
			return gzip.NewReader(in)
		}
		newWriter = func(out io.Writer) io.WriteCloser {
			return gzip.NewWriter(out)
		}
	case "br":
		newReader = func(in io.Reader) (out io.Reader, err error) {
			return brotli.NewReader(in), nil
		}
		newWriter = func(out io.Writer) io.WriteCloser {
			return brotli.NewWriter(out)
		}
	case "":
		// No content encoding header found
	default:
		// Unsupported encoding
	}

	// Read the encoded body.
	encr, err := newReader(r.Body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	body, err := io.ReadAll(encr)
	if err != nil {
		return err
	}

	// Inject JavaScript
	modified := bytes.Replace(body, bytesBodyClosingTag, s.jsInjection, 1)

	// Encode the response.
	var buf bytes.Buffer
	encw := newWriter(&buf)
	_, err = encw.Write(modified)
	if err != nil {
		return err
	}
	err = encw.Close()
	if err != nil {
		return err
	}

	// Update the response.
	r.Body = io.NopCloser(&buf)
	r.ContentLength = int64(buf.Len())
	r.Header.Set("Content-Length", strconv.Itoa(buf.Len()))
	return nil
}

var bytesBodyClosingTag = []byte("</body>")

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

type passthroughWriteCloser struct {
	io.Writer
}

func (pwc passthroughWriteCloser) Close() error {
	return nil
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

func internalErr(w http.ResponseWriter, msg string, err error) {
	http.Error(w,
		fmt.Sprintf("proxy: %s: %v", msg, err),
		http.StatusInternalServerError)
}

type roundTripper struct {
	maxRetries      int
	initialDelay    time.Duration
	backoffExponent float64
}

func (rt *roundTripper) setShouldSkipResponseModificationHeader(
	r *http.Request, resp *http.Response,
) {
	// Instruct the modifyResponse function to skip modifying the response if the
	// HTTP request has come from HTMX.
	if r.Header.Get(HeaderHXRequest) != "true" {
		return
	}
	resp.Header.Set(HeaderTemplSkipModify, "true")
}

func (rt *roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	// Read and buffer the body.
	var bodyBytes []byte
	if r.Body != nil && r.Body != http.NoBody {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body.Close()
	}

	// Retry logic.
	var resp *http.Response
	var err error
	for retries := 0; retries < rt.maxRetries; retries++ {
		// Clone the request and set the body.
		req := r.Clone(r.Context())
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		// Execute the request.
		resp, err = http.DefaultTransport.RoundTrip(req)
		if err != nil {
			dur := time.Duration(math.Pow(rt.backoffExponent, float64(retries)))
			time.Sleep(rt.initialDelay * dur)
			continue
		}

		rt.setShouldSkipResponseModificationHeader(r, resp)

		return resp, nil
	}

	return nil, fmt.Errorf("max retries reached: %q", r.URL.String())
}
