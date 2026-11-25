package server_test

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romshark/templier/internal/broadcaster"
	"github.com/romshark/templier/internal/server"
	"github.com/romshark/templier/internal/statetrack"

	"github.com/alecthomas/assert/v2"
)

// newProxy starts a templier server proxying to the app server at addr.
func newProxy(t *testing.T, addr string, proxyTimeout time.Duration) *httptest.Server {
	t.Helper()
	appHost, err := url.Parse("http://" + addr)
	assert.NoError(t, err)
	ts := httptest.NewServer(server.New(
		statetrack.NewTracker(0),
		broadcaster.NewSignalBroadcaster(),
		slog.Default(),
		server.Config{AppHost: appHost, ProxyTimeout: proxyTimeout},
	))
	t.Cleanup(ts.Close)
	return ts
}

// closedAddr returns an address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	addr := l.Addr().String()
	assert.NoError(t, l.Close())
	return addr
}

func TestProxyGivesUpAfterTimeout(t *testing.T) {
	ts := newProxy(t, closedAddr(t), 200*time.Millisecond)

	client := &http.Client{Timeout: 3 * time.Second}
	start := time.Now()
	resp, err := client.Get(ts.URL)
	assert.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.True(t, time.Since(start) < 2*time.Second,
		"took %s despite a proxy timeout of 200ms", time.Since(start))
}

func TestProxyStopsRetryingWhenClientGivesUp(t *testing.T) {
	ts := newProxy(t, closedAddr(t), time.Minute)

	client := &http.Client{Timeout: 200 * time.Millisecond}
	_, err := client.Get(ts.URL)
	assert.Error(t, err)

	// Close waits for the handler to return.
	closed := make(chan struct{})
	go func() {
		ts.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy kept retrying after the client gave up")
	}
}

func TestProxyRetriesUntilAppServerIsUp(t *testing.T) {
	addr := closedAddr(t)
	ts := newProxy(t, addr, 5*time.Second)

	app := &http.Server{Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "ok") },
	)}
	t.Cleanup(func() { _ = app.Close() })
	go func() {
		time.Sleep(300 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Errorf("starting app server: %v", err)
			return
		}
		_ = app.Serve(l)
	}()

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(ts.URL)
	assert.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok", string(body))
}

func TestProxyHEAD(t *testing.T) {
	for _, encoding := range []string{"", "gzip"} {
		app := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				if encoding != "" {
					w.Header().Set("Content-Encoding", encoding)
				}
				_, _ = fmt.Fprint(w, "<html><head></head></html>") // Not sent for HEAD.
			},
		))
		t.Cleanup(app.Close)
		ts := newProxy(t, strings.TrimPrefix(app.URL, "http://"), time.Second)

		resp, err := http.Head(ts.URL)
		assert.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "encoding %q", encoding)
		assert.Equal(t, "", resp.Header.Get("Content-Length"), "encoding %q", encoding)
	}
}

func TestProxyDoesntResendRequests(t *testing.T) {
	// The app server reads each request, then drops the connection.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	var requests atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			if r, err := http.ReadRequest(bufio.NewReader(c)); err == nil {
				_, _ = io.Copy(io.Discard, r.Body)
				requests.Add(1)
			}
			_ = c.Close()
		}
	}()
	ts := newProxy(t, l.Addr().String(), time.Minute)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(ts.URL, "text/plain", strings.NewReader("hello"))
	assert.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Equal(t, int32(1), requests.Load())
}
