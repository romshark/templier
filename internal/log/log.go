package log

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatih/color"
	"github.com/fsnotify/fsnotify"
)

var (
	lock  sync.Mutex
	out   io.Writer
	level atomic.Int32

	fBlueUnderline = color.New(color.FgBlue, color.Underline)
	fBlue          = color.New(color.FgBlue)
	fGreen         = color.New(color.FgGreen, color.Bold)
	fRed           = color.New(color.FgHiRed, color.Bold)
	fYellow        = color.New(color.FgHiYellow, color.Bold)
)

const LinePrefix = "🤖 "

// ClearLogs clears the console.
func ClearLogs() {
	switch runtime.GOOS {
	case "linux", "darwin":
		cmd := exec.Command("clear")
		cmd.Stdout = os.Stdout
		_ = cmd.Run() // Ignore errors, we don't care if it fails.
	case "windows":
		cmd := exec.Command("cmd", "/c", "cls")
		cmd.Stdout = os.Stdout
		_ = cmd.Run() // Ignore errors, we don't care if it fails.
	}
}

func init() {
	out = os.Stdout
}

type LogLevel int32

const (
	LogLevelErrOnly LogLevel = 0
	LogLevelVerbose LogLevel = 1
	LogLevelDebug   LogLevel = 2
)

const TimeFormat = "3:04:05.000 PM"

func SetLogLevel(l LogLevel) { level.Store(int32(l)) }

func Level() LogLevel { return LogLevel(level.Load()) }

// TemplierStarted prints the Templiér started log to console.
func TemplierStarted(baseURL string) {
	if Level() < LogLevelVerbose {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	if Level() >= LogLevelDebug {
		fmt.Fprint(out, time.Now().Format(TimeFormat))
		fmt.Fprint(out, " INFO: ")
	}
	fmt.Fprint(out, "Templiér ")
	fGreen.Fprint(out, "started")
	fmt.Fprint(out, " on ")
	fBlueUnderline.Fprintln(out, baseURL)
}

// TemplierRestartingServer prints the server restart trigger log to console.
func TemplierRestartingServer(cmdServerPath string) {
	if Level() < LogLevelVerbose {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	if Level() >= LogLevelDebug {
		fmt.Fprint(out, time.Now().Format(TimeFormat))
		fmt.Fprint(out, " INFO: ")
	}
	fmt.Fprint(out, "restarting ")
	fGreen.Fprintln(out, cmdServerPath)
}

// TemplierFileChange prints a file change log to console.
func TemplierFileChange(e fsnotify.Event) {
	if Level() < LogLevelVerbose {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	if Level() >= LogLevelDebug {
		fmt.Fprint(out, time.Now().Format(TimeFormat))
		fmt.Fprint(out, " INFO: ")
	}
	fmt.Fprint(out, "file ")
	fmt.Fprint(out, fileOpStr(e.Op))
	fmt.Fprint(out, ": ")
	fBlueUnderline.Fprintln(out, e.Name)
}

// Debugf prints an info line to console.
func Debugf(f string, v ...any) {
	if Level() < LogLevelDebug {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	fmt.Fprint(out, time.Now().Format(TimeFormat))
	fmt.Fprint(out, " DEBUG: ")
	fmt.Fprintf(out, f, v...)
	fmt.Fprintln(out, "")
}

// WarnUnsupportedTemplVersion prints a warning line to console
// about the currently installed templ version not matching the templ version
// that the installed version of Templier supports.
func WarnUnsupportedTemplVersion(
	templierVersion, supportedTemplVersion, currentTemplVersion string,
) {
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	fYellow.Fprint(out, " WARNING: ")
	fmt.Fprint(out, "Templier ")
	fGreen.Fprintf(out, "v%s", templierVersion)
	fmt.Fprint(out, " is optimized to work with templ ")
	fGreen.Fprintf(out, "%s. ", supportedTemplVersion)
	fmt.Fprint(out, "You're using templ ")
	fGreen.Fprint(out, currentTemplVersion)
	fmt.Fprintln(out, ". This can lead to unexpected behavior!")
}

// Infof prints an info line to console.
func Infof(f string, v ...any) {
	if Level() < LogLevelVerbose {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	if Level() >= LogLevelDebug {
		fmt.Fprint(out, time.Now().Format(TimeFormat))
		fmt.Fprint(out, " INFO: ")
	}
	fmt.Fprintf(out, f, v...)
	fmt.Fprintln(out, "")
}

// Errorf prints an error line to console.
func Error(msg string) {
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	if Level() >= LogLevelDebug {
		fmt.Fprint(out, time.Now().Format(TimeFormat))
		fmt.Fprint(out, " ")
	}
	fRed.Fprint(out, "ERR: ")
	fmt.Fprint(out, msg)
	fmt.Fprintln(out, "")
}

// Errorf is similar to Error but with formatting.
func Errorf(f string, v ...any) {
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	if Level() >= LogLevelDebug {
		fmt.Fprint(out, time.Now().Format(TimeFormat))
		fmt.Fprint(out, " ")
	}
	fRed.Fprint(out, "ERR: ")
	fmt.Fprintf(out, f, v...)
	fmt.Fprintln(out, "")
}

// FatalCmdNotAvailable prints an error line to console about
// a cmd that's required for Templiér to run not being available
// and exits process with error code 1.
func FatalCmdNotAvailable(cmd, helpURL string) {
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	if Level() >= LogLevelDebug {
		fmt.Fprint(out, time.Now().Format(TimeFormat))
		fmt.Fprint(out, " ")
	}
	fRed.Fprint(out, "ERR: ")
	fmt.Fprint(out, "it appears ")
	fGreen.Fprint(out, cmd)
	fmt.Fprintf(out, " isn't installed on your system or is not in your PATH.\n See:")
	fBlueUnderline.Fprint(out, helpURL)
	fmt.Fprintln(out, "")
	os.Exit(1)
}

// FatalCustomWatcherCmdNotAvailable prints an error line to console about
// a cmd that's required for a custom watcher to run not being available
// and exits process with error code 1.
func FatalCustomWatcherCmdNotAvailable(cmd, customWatcherName string) {
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	if Level() >= LogLevelDebug {
		fmt.Fprint(out, time.Now().Format(TimeFormat))
		fmt.Fprint(out, " ")
	}
	fRed.Fprint(out, "ERR: ")
	fmt.Fprint(out, "it appears ")
	fGreen.Fprint(out, cmd)
	fmt.Fprintf(out, " isn't installed on your system or is not in your PATH.\n")
	fmt.Fprint(out, "This command is required by custom watcher ")
	fBlue.Fprint(out, customWatcherName)
	fmt.Fprintln(out, ".")
	fmt.Fprintln(out, "")
	os.Exit(1)
}

// Fatalf prints an error line to console and exits process with error code 1.
func Fatalf(f string, v ...any) {
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	if Level() >= LogLevelDebug {
		fmt.Fprint(out, time.Now().Format(TimeFormat))
		fmt.Fprint(out, " ")
	}
	fRed.Fprint(out, "ERR: ")
	fmt.Fprintf(out, f, v...)
	fmt.Fprintln(out, "")
	os.Exit(1)
}

// Durf prints an error.
func Durf(msg string, d time.Duration) {
	if Level() < LogLevelVerbose {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(out, LinePrefix)
	if Level() >= LogLevelDebug {
		fmt.Fprint(out, time.Now().Format(TimeFormat))
		fmt.Fprint(out, ": ")
	}
	fmt.Fprint(out, msg)
	fmt.Fprint(out, " (")
	fRed.Fprint(out, durStr(d))
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
