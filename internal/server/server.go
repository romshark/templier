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
	"github.com/romshark/templier/internal/config"
	"github.com/romshark/templier/internal/log"
	"github.com/romshark/templier/internal/statetrack"
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
	config            *config.Config
	httpClient        *http.Client
	stateTracker      *statetrack.Tracker
	reload            *broadcaster.SignalBroadcaster
	jsInjection       []byte
	webSocketUpgrader websocket.Upgrader
	reverseProxy      *httputil.ReverseProxy
}

func New(
	httpClient *http.Client,
	stateTracker *statetrack.Tracker,
	reload *broadcaster.SignalBroadcaster,
	config *config.Config,
) *Server {
	var jsInjectionBuf bytes.Buffer
	err := jsInjection(config.Log.PrintJSDebugLogs, PathProxyEvents).Render(
		context.Background(), &jsInjectionBuf,
	)
	if err != nil {
		panic(fmt.Errorf("rendering the live reload injection template: %w", err))
	}
	_, _ = jsInjectionBuf.Write(bytesBodyClosingTag)
	s := &Server{
		config:       config,
		httpClient:   httpClient,
		stateTracker: stateTracker,
		reload:       reload,
		jsInjection:  jsInjectionBuf.Bytes(),
		webSocketUpgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true }, // Ignore CORS
		},
	}
	s.reverseProxy = httputil.NewSingleHostReverseProxy(config.App.Host.URL)
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
	if s.stateTracker.ErrIndex() != -1 {
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
		log.Errorf("upgrading to websocket: %v", err)
		internalErr(w, "upgrading to websocket", err)
		return
	}
	defer c.Close()

	notifyStateChange := make(chan struct{})
	s.stateTracker.AddListener(notifyStateChange)

	notifyReload := make(chan struct{})
	s.reload.AddListener(notifyReload)

	ctx, cancel := context.WithCancel(r.Context())

	defer func() {
		s.stateTracker.RemoveListener(notifyStateChange)
		s.reload.RemoveListener(notifyReload)
		cancel()
		log.Debugf("websockets: disconnect (%p); %d active listener(s)",
			c, s.stateTracker.NumListeners())
	}()

	log.Debugf(
		"websockets: upgrade connection (%p): %q; %d active listener(s)",
		c, r.URL.String(), s.stateTracker.NumListeners())

	go func() {
		defer cancel()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				break
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-notifyStateChange:
			log.Debugf("websockets: notify state change (%p)", c)
			if !writeWSMsg(c, bytesMsgReload) {
				return // Disconnect
			}
		case <-notifyReload:
			log.Debugf("websockets: notify reload (%p)", c)
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

var bytesMsgReload = []byte("r")

type Report struct{ Subject, Body string }

func (s *Server) newReports() []Report {
	if m := s.stateTracker.Get(statetrack.IndexTempl); m != "" {
		return []Report{{Subject: "templ", Body: m}}
	}
	var report []Report
	if m := s.stateTracker.Get(statetrack.IndexGolangciLint); m != "" {
		report = append(report, Report{Subject: "golangci-lint", Body: m})
	}
	if m := s.stateTracker.Get(statetrack.IndexGo); m != "" {
		report = append(report, Report{Subject: "go", Body: m})
	}
	if m := s.stateTracker.Get(statetrack.IndexExit); m != "" {
		return []Report{{Subject: "process", Body: m}}
	}
	for i, w := range s.config.CustomWatchers {
		if m := s.stateTracker.Get(statetrack.IndexOffsetCustomWatcher + i); m != "" {
			report = append(report, Report{Subject: string(w.Name), Body: m})
		}
	}
	return report
}

func (s *Server) handleErrPage(w http.ResponseWriter, r *http.Request) {
	reports := s.newReports()

	title := "1 error"
	if len(reports) > 1 {
		title = strconv.Itoa(len(reports)) + " errors"
	}

	comp := errpage(title, reports, s.config.Log.PrintJSDebugLogs, PathProxyEvents)
	if err := comp.Render(r.Context(), w); err != nil {
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
