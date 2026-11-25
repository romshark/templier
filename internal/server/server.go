package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"github.com/romshark/templier/internal/broadcaster"
	"github.com/romshark/templier/internal/statetrack"

	"github.com/andybalholm/brotli"
	"github.com/gorilla/websocket"
)

const (
	HeaderHXRequest       = "HX-Request"
	HeaderDatastarRequest = "Datastar-Request"
	HeaderTemplSkipModify = "templ-skip-modify"

	// PathProxyEvents defines the path for templier proxy websocket events endpoint.
	// This path is very unlikely to collide with any path used by the app server.
	PathProxyEvents = "/__templier/events"

	// ReverseProxyRetryInterval is how long the proxy waits
	// before trying to connect to the app server again.
	ReverseProxyRetryInterval = 100 * time.Millisecond
)

type Config struct {
	PrintJSDebugLogs   bool
	AppHost            *url.URL
	ProxyTimeout       time.Duration
	CustomWatcherNames []string
	ReconnectMessage   string
}

type Server struct {
	config            Config
	logger            *slog.Logger
	stateTracker      *statetrack.Tracker
	reload            *broadcaster.SignalBroadcaster
	jsInjection       []byte
	webSocketUpgrader websocket.Upgrader
	reverseProxy      *httputil.ReverseProxy
}

func MustRenderJSInjection(
	ctx context.Context, printJSDebugLogs bool, reconnectMessage string,
) []byte {
	var buf bytes.Buffer
	err := jsInjection(printJSDebugLogs, PathProxyEvents, reconnectMessage).Render(ctx, &buf)
	if err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func RenderErrpage(
	ctx context.Context, w io.Writer,
	title string, content []Report,
	printJSDebugLogs bool, reconnectMessage string,
) error {
	return pageError(title, content, printJSDebugLogs, PathProxyEvents, reconnectMessage).Render(ctx, w)
}

func New(
	stateTracker *statetrack.Tracker,
	reload *broadcaster.SignalBroadcaster,
	logger *slog.Logger,
	conf Config,
) *Server {
	jsInjection := MustRenderJSInjection(
		context.Background(), conf.PrintJSDebugLogs, conf.ReconnectMessage,
	)
	s := &Server{
		config:       conf,
		logger:       logger,
		stateTracker: stateTracker,
		reload:       reload,
		jsInjection:  jsInjection,
		webSocketUpgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true }, // Ignore CORS
		},
	}
	s.reverseProxy = httputil.NewSingleHostReverseProxy(conf.AppHost)
	s.reverseProxy.Transport = &roundTripper{
		timeout:       conf.ProxyTimeout,
		retryInterval: ReverseProxyRetryInterval,
	}
	s.reverseProxy.ModifyResponse = s.modifyResponse
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == PathProxyEvents {
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

	// Skip modification for responses that carry no body (1xx, 204, 304).
	// A 304 with Content-Encoding: gzip but an empty body would otherwise
	// cause gzip.NewReader to fail with EOF, turning the response into a 502.
	// In a browser this would otherwise create an endless loop:
	// 200 -> 304 -> 502 -> 200 -> 304 -> 502 -> ...
	if r.StatusCode < 200 ||
		r.StatusCode == http.StatusNoContent ||
		r.StatusCode == http.StatusNotModified {
		return nil
	}

	// Skip modification if Content-Type is text/event-stream (SSE)
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
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
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(encr)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	encw := newWriter(&buf)
	if strings.HasPrefix(http.DetectContentType(body), "text/html") {
		// Inject JavaScript
		if err = WriteWithInjection(encw, body, s.jsInjection); err != nil {
			return fmt.Errorf("injecting JS: %w", err)
		}
	} else if _, err := encw.Write(body); err != nil {
		return err
	}

	if err := encw.Close(); err != nil {
		return err
	}

	// Update response body.
	r.Body = io.NopCloser(&buf)
	r.ContentLength = int64(buf.Len())
	r.Header.Set("Content-Length", strconv.Itoa(buf.Len()))
	return nil
}

// closingTags are the tags the injection is placed in front of, in order of preference.
var closingTags = [][]byte{
	[]byte("</head>"), []byte("</HEAD>"), []byte("</body>"), []byte("</BODY>"),
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
		s.logger.Error("upgrading to websocket", "err", err)
		internalErr(w, "upgrading to websocket", err)
		return
	}
	defer func() { _ = c.Close() }()

	notifyStateChange := make(chan struct{})
	s.stateTracker.AddListener(notifyStateChange)

	notifyReload := make(chan struct{})
	s.reload.AddListener(notifyReload)

	ctx, cancel := context.WithCancel(r.Context())

	defer func() {
		s.stateTracker.RemoveListener(notifyStateChange)
		s.reload.RemoveListener(notifyReload)
		cancel()
		s.logger.Debug("websockets: disconnect",
			"listeners", s.stateTracker.NumListeners())
	}()

	s.logger.Debug("websockets: upgrade connection",
		"url", r.URL.String(), "listeners", s.stateTracker.NumListeners())

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
			s.logger.Debug("websockets: notify state change")
			if !writeWSMsg(c, bytesMsgReload) {
				return // Disconnect
			}
		case <-notifyReload:
			s.logger.Debug("websockets: notify reload")
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
	for i, name := range s.config.CustomWatcherNames {
		if m := s.stateTracker.Get(statetrack.IndexOffsetCustomWatcher + i); m != "" {
			report = append(report, Report{Subject: name, Body: m})
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

	err := RenderErrpage(r.Context(), w, title, reports, s.config.PrintJSDebugLogs, s.config.ReconnectMessage)
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
	// timeout is how long to keep trying to connect to the app server.
	timeout       time.Duration
	retryInterval time.Duration
}

func (rt *roundTripper) setShouldSkipResponseModificationHeader(
	r *http.Request, resp *http.Response,
) {
	// Instruct the modifyResponse function to skip modifying the response if the
	// HTTP request has come from HTMX.
	if r.Header.Get(HeaderHXRequest) == "true" ||
		r.Header.Get(HeaderDatastarRequest) == "true" {
		resp.Header.Set(HeaderTemplSkipModify, "true")
	}
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
		_ = r.Body.Close()
	}

	// The app server may be restarting, so keep trying to connect until
	// the timeout passes or the client gives up.
	deadline := time.Now().Add(rt.timeout)
	for {
		// Clone the request and set the body.
		req := r.Clone(r.Context())
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := http.DefaultTransport.RoundTrip(req)
		if err == nil {
			rt.setShouldSkipResponseModificationHeader(r, resp)
			return resp, nil
		}

		// Only retry if connecting failed.
		// Otherwise the app server may have received the request already.
		var opErr *net.OpError
		wait := min(rt.retryInterval, time.Until(deadline))
		if !errors.As(err, &opErr) || opErr.Op != "dial" || wait <= 0 {
			return nil, err
		}
		select {
		case <-time.After(wait):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
}

// WriteWithInjection writes to w the body with the injection either at the end of the
// head or at the end of the body. If neither the head nor the body closing tag is found,
// the injection is written to w before body.
// injection is shared by concurrent responses and must not be modified.
func WriteWithInjection(
	w io.Writer, body []byte, injection []byte,
) error {
	at := 0
	for _, tag := range closingTags {
		if i := bytes.Index(body, tag); i != -1 {
			at = i
			break
		}
	}
	for _, b := range [][]byte{body[:at], injection, body[at:]} {
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}
