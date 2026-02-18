package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/fatih/color"
)

// Handler is an [slog.Handler] that produces the same pretty colored output
// as the original templier log package.
type Handler struct {
	mu  sync.Mutex
	out io.Writer

	level      slog.Leveler
	attrs      []slog.Attr
	group      string
	linePrefix string
	timeFormat string
}

var _ slog.Handler = (*Handler)(nil)

// NewHandler creates a new colored [slog.Handler] writing to out.
// The level controls which messages are emitted.
// LinePrefix defaults to "🤖 " and TimeFormat defaults to "3:04:05.000 PM".
func NewHandler(out io.Writer, level slog.Leveler) *Handler {
	return &Handler{
		out:        out,
		level:      level,
		linePrefix: "🤖 ",
		timeFormat: "15:04:05",
	}
}

// SetLinePrefix sets the prefix printed at the beginning of each log line.
func (h *Handler) SetLinePrefix(prefix string) { h.linePrefix = prefix }

// SetTimeFormat sets the time format string used for timestamps.
func (h *Handler) SetTimeFormat(format string) { h.timeFormat = format }

func (h *Handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	_, _ = fmt.Fprint(h.out, h.linePrefix)

	// Timestamps shown at debug level.
	if h.level.Level() <= slog.LevelDebug {
		_, _ = fGrey.Fprint(h.out, r.Time.Format(h.timeFormat))
		_, _ = fmt.Fprint(h.out, " ")
	}

	switch {
	case r.Level >= slog.LevelError:
		_, _ = fRedBold.Fprint(h.out, "ERR: ")
	case r.Level >= slog.LevelWarn:
		_, _ = fYellowBold.Fprint(h.out, "WARN: ")
	case r.Level >= slog.LevelInfo:
		if h.level.Level() <= slog.LevelDebug {
			_, _ = fGreenBold.Fprint(h.out, "INFO: ")
		}
	default: // Debug
		_, _ = fmt.Fprint(h.out, "DEBUG: ")
	}

	_, _ = fmt.Fprint(h.out, r.Message)

	// Print attributes.
	r.Attrs(func(a slog.Attr) bool {
		h.writeAttr(a)
		return true
	})
	for _, a := range h.attrs {
		h.writeAttr(a)
	}

	_, _ = fmt.Fprintln(h.out)
	return nil
}

func (h *Handler) writeAttr(a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if h.group != "" {
		key = h.group + "." + key
	}

	// Special formatting for duration values.
	if key == "duration" {
		if d, ok := a.Value.Any().(time.Duration); ok {
			_, _ = fmt.Fprint(h.out, " (")
			_, _ = fRedBold.Fprint(h.out, DurStr(d))
			_, _ = fmt.Fprint(h.out, ")")
			return
		}
	}

	_, _ = fmt.Fprintf(h.out, " %s=%s", fBlue.Sprint(key), fGreen.Sprint(a.Value.String()))
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		out:        h.out,
		level:      h.level,
		attrs:      append(h.attrs[:len(h.attrs):len(h.attrs)], attrs...),
		group:      h.group,
		linePrefix: h.linePrefix,
		timeFormat: h.timeFormat,
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &Handler{
		out:        h.out,
		level:      h.level,
		attrs:      h.attrs,
		group:      g,
		linePrefix: h.linePrefix,
		timeFormat: h.timeFormat,
	}
}

// ClearLogs clears the console.
func ClearLogs() {
	switch runtime.GOOS {
	case "linux", "darwin":
		cmd := exec.Command("clear")
		cmd.Stdout = os.Stdout
		_ = cmd.Run()
	case "windows":
		cmd := exec.Command("cmd", "/c", "cls")
		cmd.Stdout = os.Stdout
		_ = cmd.Run()
	}
}

// Fatalf prints an error line to stderr and exits with code 1.
func Fatalf(f string, v ...any) {
	h := &Handler{out: os.Stderr, level: &slog.LevelVar{}}
	logger := slog.New(h)
	logger.Error(fmt.Sprintf(f, v...))
	os.Exit(1)
}

// DurStr formats a duration in a human-friendly way.
func DurStr(d time.Duration) string {
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%.0fns", float64(d)/float64(time.Nanosecond))
	case d < time.Millisecond:
		return fmt.Sprintf("%.0fµs", float64(d)/float64(time.Microsecond))
	case d < time.Second:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", float64(d)/float64(time.Second))
	}
	return d.String()
}

var (
	fRedBold    = color.New(color.FgHiRed, color.Bold)
	fYellowBold = color.New(color.FgHiYellow, color.Bold)
	fGreenBold  = color.New(color.FgGreen, color.Bold)
	fGreen      = color.New(color.FgGreen)
	fBlue       = color.New(color.FgBlue)
	fGrey       = color.New(color.FgHiBlack)
)
