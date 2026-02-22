// Package engine provides the core templier engine for programmatic use.
// It allows running templier as a library without the CLI.
package engine

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	Version               = "0.11.1"
	SupportedTemplVersion = "v0.3.977"
)

// ActionType defines what action a custom watcher requires after a file change.
type ActionType int8

const (
	// ActionNone means no further action is required; just execute the command.
	ActionNone ActionType = iota
	// ActionReload requires browser tabs to be reloaded.
	ActionReload
	// ActionRestart requires the server process to be restarted.
	ActionRestart
	// ActionRebuild requires the server to be rebuilt and restarted.
	ActionRebuild
)

// LogLevel defines the verbosity of logging.
type LogLevel int8

const (
	// LogLevelError logs errors only.
	LogLevelError LogLevel = iota
	// LogLevelVerbose enables verbose logging of relevant events.
	LogLevelVerbose
	// LogLevelDebug enables verbose debug logging.
	LogLevelDebug
)

// LogClearOn defines when to clear logs.
type LogClearOn int8

const (
	// LogClearNever disables log clearing.
	LogClearNever LogClearOn = iota
	// LogClearOnRestart clears logs when the app server restarts.
	LogClearOnRestart
	// LogClearOnFileChange clears logs on every file change.
	LogClearOnFileChange
)

// Config is the configuration for the templier engine.
type Config struct {
	// App defines the application being developed.
	App AppConfig

	// Compiler configures the Go compiler. Nil means default compiler settings.
	Compiler *CompilerConfig

	// Debounce is the duration to wait before triggering a rebuild
	// after a file change. Zero means 50ms default.
	Debounce time.Duration

	// ProxyTimeout for proxied requests to the app server.
	// Zero means 2s default.
	ProxyTimeout time.Duration

	// Lint enables golangci-lint before building.
	Lint bool

	// Format enables automatic templ formatting on .templ file changes.
	Format bool

	// TemplierHost is the host:port for the templier proxy server.
	// Example: "127.0.0.1:9999".
	TemplierHost string

	// TLS enables TLS for the templier proxy server. Nil means plain HTTP.
	TLS *TLSConfig

	// CustomWatchers defines additional file watchers with custom commands.
	CustomWatchers []CustomWatcherConfig

	// Log configures logging behavior.
	Log LogConfig
}

// AppConfig defines the application being developed.
type AppConfig struct {
	// DirSrcRoot is the absolute path to the source root directory to watch.
	DirSrcRoot string

	// Exclude is a list of glob patterns to exclude from watching.
	Exclude []string

	// DirCmd is the path to the Go command to build (e.g., "./cmd/server/").
	DirCmd string

	// DirWork is the working directory for building and running the app server.
	DirWork string

	// Flags are flags passed to the app server binary on startup.
	Flags []string

	// Host is the app server's URL for health checks and reverse proxying.
	Host *url.URL
}

// CompilerConfig configures the Go compiler.
type CompilerConfig struct {
	// Flags are pre-assembled compiler flags
	// (e.g. ["-gcflags", "all=-N -l", "-race"]).
	Flags []string

	// Env are environment variables for the compiler (e.g. ["GOARCH=amd64"]).
	Env []string
}

// TLSConfig configures TLS for the templier proxy server.
type TLSConfig struct {
	Cert string
	Key  string
}

// LogConfig configures logging behavior.
type LogConfig struct {
	// Level controls the verbosity of logging.
	Level LogLevel

	// ClearOn defines when to clear log output.
	ClearOn LogClearOn

	// PrintJSDebugLogs enables debug logs in the injected browser JavaScript.
	PrintJSDebugLogs bool
}

// CustomWatcherConfig defines a custom file watcher with an associated command.
type CustomWatcherConfig struct {
	// Name is the display name for the custom watcher.
	Name string

	// Cmd is the shell command to run when an included file changes.
	// Executed via "sh -c". Optional if Requires is set.
	Cmd string

	// Include specifies glob patterns for what files to watch.
	Include []string

	// Exclude specifies glob patterns for what files to ignore
	// that would otherwise match Include.
	Exclude []string

	// Debounce defines how long to wait for more file changes
	// before executing Cmd. Zero means 50ms default.
	Debounce time.Duration

	// FailOnErr shows the command's error output in the browser
	// (like a build error) when the command exits with code 1.
	FailOnErr bool

	// Requires defines what action is required when an included file changes.
	Requires ActionType
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	if c.TemplierHost == "" {
		return errors.New("engine: TemplierHost is required")
	}
	if c.App.DirSrcRoot == "" {
		return errors.New("engine: App.DirSrcRoot is required")
	}
	if c.App.DirCmd == "" {
		return errors.New("engine: App.DirCmd is required")
	}
	if c.App.DirWork == "" {
		return errors.New("engine: App.DirWork is required")
	}
	if c.App.Host == nil {
		return errors.New("engine: App.Host is required")
	}
	if c.TLS != nil {
		if c.TLS.Cert == "" {
			return errors.New("engine: TLS.Cert is required when TLS is set")
		}
		if c.TLS.Key == "" {
			return errors.New("engine: TLS.Key is required when TLS is set")
		}
	}

	// Validate glob patterns.
	for i, pattern := range c.App.Exclude {
		if !doublestar.ValidatePattern(pattern) {
			return fmt.Errorf("engine: App.Exclude[%d] invalid glob pattern %q", i, pattern)
		}
	}
	for i, w := range c.CustomWatchers {
		if w.Name == "" {
			return fmt.Errorf("engine: CustomWatchers[%d] has no name", i)
		}
		if w.Requires == ActionNone && w.Cmd == "" {
			return fmt.Errorf(
				"engine: CustomWatchers[%d] %q requires no action, cmd must not be empty",
				i, w.Name,
			)
		}
		for j, pattern := range w.Include {
			if !doublestar.ValidatePattern(pattern) {
				return fmt.Errorf(
					"engine: CustomWatchers[%d].Include[%d] invalid glob pattern %q",
					i, j, pattern,
				)
			}
		}
		for j, pattern := range w.Exclude {
			if !doublestar.ValidatePattern(pattern) {
				return fmt.Errorf(
					"engine: CustomWatchers[%d].Exclude[%d] invalid glob pattern %q",
					i, j, pattern,
				)
			}
		}
	}

	// Check required binaries.
	if _, err := exec.LookPath("templ"); err != nil {
		return fmt.Errorf("engine: templ is not installed or not in PATH: %w", err)
	}
	if c.Lint {
		if _, err := exec.LookPath("golangci-lint"); err != nil {
			return fmt.Errorf(
				"engine: golangci-lint is not installed or not in PATH "+
					"(required when Lint is enabled): %w", err,
			)
		}
	}
	for _, w := range c.CustomWatchers {
		if w.Cmd == "" {
			continue
		}
		cmd := cmdFromString(w.Cmd)
		if _, err := exec.LookPath(cmd); err != nil {
			return fmt.Errorf(
				"engine: custom watcher %q command %q not found: %w",
				w.Name, cmd, err,
			)
		}
	}

	return nil
}

// cmdFromString extracts the command name (first word) from a shell command string.
func cmdFromString(s string) string {
	for i, c := range s {
		if c == ' ' || c == '\t' {
			return s[:i]
		}
	}
	return s
}

// applyDefaults fills in zero-valued fields with sensible defaults.
func (c *Config) applyDefaults() {
	if c.Debounce == 0 {
		c.Debounce = 50 * time.Millisecond
	}
	if c.ProxyTimeout == 0 {
		c.ProxyTimeout = 2 * time.Second
	}
	for i := range c.CustomWatchers {
		if c.CustomWatchers[i].Debounce == 0 {
			c.CustomWatchers[i].Debounce = 50 * time.Millisecond
		}
	}
}
