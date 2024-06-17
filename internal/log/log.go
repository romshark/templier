package log

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatih/color"
	"github.com/fsnotify/fsnotify"
)

var (
	lock    sync.Mutex
	out     io.Writer
	verbose atomic.Bool

	fBlueUnderline = color.New(color.FgBlue, color.Underline)
	fGreen         = color.New(color.FgGreen, color.Bold)
	// fCyanUnderline = color.New(color.FgCyan, color.Underline)
	fRed = color.New(color.FgHiRed, color.Bold)
)

func init() {
	out = os.Stdout
}

func SetVerbose(enable bool) { verbose.Store(enable) }

// TemplierStarted prints the Templiér started log to console.
func TemplierStarted(baseURL string) {
	if !verbose.Load() {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, "🤖 Templiér ")
	fGreen.Fprint(out, "started")
	fmt.Fprint(out, " on ")
	fBlueUnderline.Fprintln(out, baseURL)
}

// TemplierRestartingServer prints the server restart trigger log to console.
func TemplierRestartingServer(cmdServerPath string) {
	if !verbose.Load() {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, "🤖 restarting ")
	fGreen.Fprintln(out, cmdServerPath)
}

// TemplierFileChange prints a file change log to console.
func TemplierFileChange(e fsnotify.Event) {
	if !verbose.Load() {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, "🤖 file ")
	fmt.Fprint(out, fileOpStr(e.Op))
	fmt.Fprint(out, ": ")
	fBlueUnderline.Fprintln(out, e.Name)
}

// Infof prints an info line to console.
func Infof(f string, v ...any) {
	if !verbose.Load() {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, "🤖:")
	fmt.Fprintf(out, f, v...)
	fmt.Fprintln(out, "")
}

// Errorf prints an error line to console.
func Errorf(f string, v ...any) {
	lock.Lock()
	defer lock.Unlock()
	fRed.Fprint(out, "🤖 ERR:")
	fmt.Fprintf(out, f, v...)
	fmt.Fprintln(out, "")
}

// Fatalf prints an error line to console and exits process with error code 1.
func Fatalf(f string, v ...any) {
	lock.Lock()
	defer lock.Unlock()
	fRed.Fprint(out, "🤖 ERR:")
	fmt.Fprintf(out, f, v...)
	fmt.Fprintln(out, "")
	os.Exit(1)
}

// Durf prints an error.
func Durf(msg string, d time.Duration) {
	if !verbose.Load() {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, "🤖 ")
	fmt.Fprint(out, msg)
	fmt.Fprint(out, " (")
	fRed.Fprintf(out, durStr(d))
	fmt.Fprintln(out, ")")
}

func durStr(d time.Duration) string {
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

func fileOpStr(operation fsnotify.Op) string {
	switch operation {
	case fsnotify.Write:
		return "changed"
	case fsnotify.Create:
		return "created"
	case fsnotify.Remove:
		return "removed"
	case fsnotify.Rename:
		return "renamed"
	case fsnotify.Chmod:
		return "permissions changed"
	}
	return ""
}
