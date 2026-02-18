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

	ReverseProxyRetries         = 20
	ReverseProxyInitialDelay    = 100 * time.Millisecond
	ReverseProxyBackoffExponent = 1.5
)

type Config struct {
	PrintJSDebugLogs   bool
	AppHost            *url.URL
	ProxyTimeout       time.Duration
	CustomWatcherNames []string
}

type Server struct {
	config            Config
	logger            *slog.Logger
	httpClient        *http.Client
	stateTracker      *statetrack.Tracker
	reload            *broadcaster.SignalBroadcaster
	jsInjection       []byte
	webSocketUpgrader websocket.Upgrader
	reverseProxy      *httputil.ReverseProxy
}

func MustRenderJSInjection(ctx context.Context, printJSDebugLogs bool) []byte {
	var buf bytes.Buffer
	err := jsInjection(printJSDebugLogs, PathProxyEvents).Render(ctx, &buf)
	if err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func RenderErrpage(
	ctx context.Context, w io.Writer,
	title string, content []Report,
	printJSDebugLogs bool,
) error {
	return pageError(title, content, printJSDebugLogs, PathProxyEvents).Render(ctx, w)
}

func New(
	httpClient *http.Client,
	stateTracker *statetrack.Tracker,
	reload *broadcaster.SignalBroadcaster,
	logger *slog.Logger,
	conf Config,
) *Server {
	jsInjection := MustRenderJSInjection(
		context.Background(), conf.PrintJSDebugLogs,
	)
	s := &Server{
		config:       conf,
		logger:       logger,
		httpClient:   httpClient,
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
		maxRetries:      ReverseProxyRetries,
		initialDelay:    ReverseProxyInitialDelay,
		backoffExponent: ReverseProxyBackoffExponent,
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

var (
	bytesHeadClosingTag          = []byte("</head>")
	bytesHeadClosingTagUppercase = []byte("</HEAD>")
	bytesBodyClosingTag          = []byte("</body>")
	bytesBodyClosingTagUppercase = []byte("</BODY>")
)

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

	err := RenderErrpage(r.Context(), w, title, reports, s.config.PrintJSDebugLogs)
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

	// Retry logic.
	var resp *http.Response
	var err error
	for retries := range rt.maxRetries {
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

// WriteWithInjection writes to w the body with the injection either at the end of the
// head or at the end of the body. If neither the head nor the body closing tags are
// the injection is written to w before body.
func WriteWithInjection(
	w io.Writer, body []byte, injection []byte,
) error {
	if bytes.Contains(body, bytesHeadClosingTag) {
		// HEAD closing tag found, replace it.
		modified := bytes.Replace(body, bytesHeadClosingTag,
			append(injection, bytesHeadClosingTag...), 1)
		_, err := w.Write(modified)
		return err
	} else if bytes.Contains(body, bytesHeadClosingTagUppercase) {
		// Uppercase HEAD closing tag found, replace it.
		modified := bytes.Replace(body, bytesHeadClosingTagUppercase,
			append(injection, bytesHeadClosingTagUppercase...), 1)
		_, err := w.Write(modified)
		return err
	} else if bytes.Contains(body, bytesBodyClosingTag) {
		// BODY closing tag found, replace it.
		modified := bytes.Replace(body, bytesBodyClosingTag,
			append(injection, bytesBodyClosingTag...), 1)
		_, err := w.Write(modified)
		return err
	} else if bytes.Contains(body, bytesBodyClosingTagUppercase) {
		// Uppercase BODY closing tag found, replace it.
		modified := bytes.Replace(body, bytesBodyClosingTagUppercase,
			append(injection, bytesBodyClosingTagUppercase...), 1)
		_, err := w.Write(modified)
		return err
	}

	// Prepend the injection to the body.
	if _, err := w.Write(injection); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}
